// Package apitypes generates TypeScript declarations for the JSON API from
// the Go source, so the UI's type check (web/tsconfig.json) fails when the
// UI reads a field the server no longer sends.
//
// It loads internal/api with go/types, finds each route registered on an
// http.ServeMux, and follows the route's handler (and the same-package helpers
// it passes its ResponseWriter or Request to) to its writeJSON and readJSON
// calls. The static types of their arguments become the route's response and
// request body; the structs they reach become TypeScript interfaces, named
// and shaped as encoding/json would encode them. A map literal with constant
// keys (map[string]any{"ok": true}) becomes an object type with those keys.
package apitypes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Generate returns the declarations for the package at pattern (e.g.
// "./internal/api"), resolved from dir.
func Generate(dir, pattern string) ([]byte, error) {
	p, err := load(dir, pattern)
	if err != nil {
		return nil, err
	}
	g := &gen{p: p, names: map[*types.TypeName]string{}, taken: map[string]bool{}, decls: map[string]string{}}
	routes, err := g.routes()
	if err != nil {
		return nil, err
	}
	return g.render(routes), nil
}

type pkg struct {
	fset  *token.FileSet
	files []*ast.File
	types *types.Package
	info  *types.Info
	decls map[*types.Func]*ast.FuncDecl
}

// load type-checks the package from source, importing its dependencies from
// the compiler's export data (go list -export), so nothing outside the
// standard library is needed.
func load(dir, pattern string) (*pkg, error) {
	cmd := exec.Command("go", "list", "-export", "-deps", "-json=ImportPath,Name,Dir,GoFiles,Export,DepOnly,Error", pattern)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	type listed struct {
		ImportPath, Name, Dir, Export string
		GoFiles                       []string
		DepOnly                       bool
		Error                         *struct{ Err string }
	}
	exports := map[string]string{}
	var target *listed
	for dec := json.NewDecoder(bytes.NewReader(out)); dec.More(); {
		var l listed
		if err := dec.Decode(&l); err != nil {
			return nil, err
		}
		if l.Error != nil {
			return nil, fmt.Errorf("%s: %s", l.ImportPath, l.Error.Err)
		}
		exports[l.ImportPath] = l.Export
		if !l.DepOnly {
			if target != nil {
				return nil, fmt.Errorf("%s matches more than one package", pattern)
			}
			target = &l
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%s matches no package", pattern)
	}
	p := &pkg{fset: token.NewFileSet(), decls: map[*types.Func]*ast.FuncDecl{}}
	for _, name := range target.GoFiles {
		f, err := parser.ParseFile(p.fset, filepath.Join(target.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, f)
	}
	conf := types.Config{Importer: importer.ForCompiler(p.fset, "gc", func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})}
	p.info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	if p.types, err = conf.Check(target.ImportPath, p.fset, p.files, p.info); err != nil {
		return nil, err
	}
	for _, f := range p.files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				if fn, ok := p.info.Defs[fd.Name].(*types.Func); ok {
					p.decls[fn] = fd
				}
			}
		}
	}
	return p, nil
}

type route struct {
	method, path string
	resp, body   []string    // TypeScript types; a route can answer in more than one shape
	shapes       [][]tsField // responses that are map literals, merged into one object type
}

// tsField is an object type's property; alts are its possible types.
type tsField struct {
	name string
	alts []string
	opt  bool
}

func (f *tsField) add(ts string) {
	parts := []string{ts}
	if !strings.ContainsAny(ts, "{(<") {
		parts = strings.Split(ts, " | ") // Item | null and Item make Item | null
	}
	for _, p := range parts {
		if !slices.Contains(f.alts, p) {
			f.alts = append(f.alts, p)
		}
	}
}

type gen struct {
	p     *pkg
	names map[*types.TypeName]string // Go named struct → its interface's name
	taken map[string]bool
	decls map[string]string // interface name → declaration
	order []string
}

// routes finds mux.Handle / mux.HandleFunc("METHOD /path", ...) calls.
func (g *gen) routes() ([]route, error) {
	var out []route
	var err error
	for _, f := range g.p.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 || err != nil {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") || !isNamed(g.p.info.TypeOf(sel.X), "net/http", "ServeMux") {
				return true
			}
			tv := g.p.info.Types[call.Args[0]]
			if tv.Value == nil || tv.Value.Kind() != constant.String {
				return true
			}
			method, path, found := strings.Cut(constant.StringVal(tv.Value), " ")
			if !found {
				method, path = "", method
			}
			// Only the API; a pattern ending in / is a catch-all for a subtree.
			if !strings.HasPrefix(path, "/api/") || strings.HasSuffix(path, "/") {
				return true
			}
			r := route{method: method, path: path}
			if lit, ok := ast.Unparen(call.Args[1]).(*ast.FuncLit); ok {
				g.collectBody(lit.Body, map[*types.Func]bool{}, &r)
			} else if h := g.handler(call.Args[1]); h != nil {
				g.collect(h, map[*types.Func]bool{}, &r)
			} else {
				err = fmt.Errorf("%s: can't find the handler for %q", g.p.fset.Position(call.Pos()), path)
				return false
			}
			out = append(out, r)
			return true
		})
	}
	return out, err
}

// handler finds the method or function value with a handler's signature in
// a registration's second argument (e.g. s.me in u(s.me)).
func (g *gen) handler(e ast.Expr) *types.Func {
	var fn *types.Func
	ast.Inspect(e, func(n ast.Node) bool {
		if fn != nil {
			return false
		}
		var id *ast.Ident
		switch x := n.(type) {
		case *ast.SelectorExpr:
			id = x.Sel
		case *ast.Ident:
			id = x
		default:
			return true
		}
		if f, ok := g.p.info.Uses[id].(*types.Func); ok && g.p.decls[f] != nil && isHandler(f) {
			fn = f
		}
		return true
	})
	return fn
}

func isHandler(f *types.Func) bool {
	sig := f.Signature()
	return sig.Params().Len() == 2 && isNamed(sig.Params().At(0).Type(), "net/http", "ResponseWriter") &&
		isNamed(sig.Params().At(1).Type(), "net/http", "Request")
}

// collect adds the types fn (and the helpers it hands its ResponseWriter or
// Request to) writes with writeJSON and reads with readJSON.
func (g *gen) collect(fn *types.Func, seen map[*types.Func]bool, r *route) {
	if seen[fn] {
		return
	}
	seen[fn] = true
	g.collectBody(g.p.decls[fn].Body, seen, r)
}

func (g *gen) collectBody(body *ast.BlockStmt, seen map[*types.Func]bool, r *route) {
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee := g.callee(call)
		if callee == nil || callee.Pkg() != g.p.types || g.p.decls[callee] == nil {
			return true
		}
		switch {
		case callee.Name() == "writeJSON" && len(call.Args) == 2:
			if shape := g.mapShape(call.Args[1], body); shape != nil {
				r.shapes = append(r.shapes, shape)
			} else {
				r.resp = appendNew(r.resp, g.ts(g.p.info.TypeOf(call.Args[1])))
			}
		case callee.Name() == "readJSON" && len(call.Args) == 2:
			if ptr, ok := g.p.info.TypeOf(call.Args[1]).(*types.Pointer); ok {
				r.body = appendNew(r.body, g.ts(ptr.Elem()))
			}
		case g.passesHTTP(call):
			g.collect(callee, seen, r)
		}
		return true
	})
}

func (g *gen) callee(call *ast.CallExpr) *types.Func {
	var id *ast.Ident
	switch f := call.Fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil
	}
	fn, _ := g.p.info.Uses[id].(*types.Func)
	return fn
}

// passesHTTP reports whether a call hands on the handler's ResponseWriter or
// Request, i.e. may write the response or read the body.
func (g *gen) passesHTTP(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		t := g.p.info.TypeOf(a)
		if isNamed(t, "net/http", "ResponseWriter") || isNamed(t, "net/http", "Request") {
			return true
		}
	}
	return false
}

// mapShape is the object type of a writeJSON argument that is a map built
// from a literal with constant keys (plus constant-key assignments in the
// same function, which become optional), or nil for any other value.
func (g *gen) mapShape(e ast.Expr, body *ast.BlockStmt) []tsField {
	if _, ok := g.p.info.TypeOf(e).Underlying().(*types.Map); !ok {
		return nil
	}
	lit, ok := ast.Unparen(e).(*ast.CompositeLit)
	var obj types.Object
	if !ok {
		id, isID := ast.Unparen(e).(*ast.Ident)
		if !isID {
			return nil
		}
		obj = g.p.info.Uses[id]
		if lit = g.definingLit(obj, body); lit == nil {
			return nil
		}
	}
	var fields []tsField
	add := func(k ast.Expr, v ast.Expr, opt bool) bool {
		tv := g.p.info.Types[k]
		if tv.Value == nil || tv.Value.Kind() != constant.String {
			return false
		}
		name := constant.StringVal(tv.Value)
		ts := g.ts(g.p.info.TypeOf(v))
		for i := range fields {
			if fields[i].name == name {
				fields[i].add(ts)
				return true
			}
		}
		fields = append(fields, tsField{name: name, alts: []string{ts}, opt: opt})
		return true
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok || !add(kv.Key, kv.Value, false) {
			return nil
		}
	}
	if obj != nil {
		dynamic := false
		ast.Inspect(body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != len(as.Rhs) {
				return true
			}
			for i, l := range as.Lhs {
				ix, ok := l.(*ast.IndexExpr)
				if !ok {
					continue
				}
				if id, ok := ix.X.(*ast.Ident); ok && g.p.info.Uses[id] == obj && !add(ix.Index, as.Rhs[i], true) {
					dynamic = true
				}
			}
			return true
		})
		if dynamic {
			return nil
		}
	}
	if fields == nil {
		fields = []tsField{}
	}
	return fields
}

// merge combines a handler's response shapes into one object type: a
// property is required only if every shape has it. JavaScript reads the
// response without telling the shapes apart.
func merge(shapes [][]tsField) []tsField {
	var out []tsField
	for _, shape := range shapes {
		for _, f := range shape {
			i := slices.IndexFunc(out, func(o tsField) bool { return o.name == f.name })
			if i < 0 {
				out = append(out, tsField{name: f.name, opt: f.opt})
				i = len(out) - 1
			}
			for _, a := range f.alts {
				out[i].add(a)
			}
			out[i].opt = out[i].opt || f.opt
		}
	}
	for i := range out {
		for _, shape := range shapes {
			if !slices.ContainsFunc(shape, func(f tsField) bool { return f.name == out[i].name }) {
				out[i].opt = true
			}
		}
	}
	return out
}

// inline renders an object type on one line.
func inline(fields []tsField) string {
	if len(fields) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{ ")
	for _, f := range fields {
		fmt.Fprintf(&b, "%s%s: %s; ", tsKey(f.name), optMark(f.opt), strings.Join(f.alts, " | "))
	}
	b.WriteString("}")
	return b.String()
}

func optMark(opt bool) string {
	if opt {
		return "?"
	}
	return ""
}

// definingLit finds the composite literal obj was declared with in fn
// (x := map[string]any{...}).
func (g *gen) definingLit(obj types.Object, body *ast.BlockStmt) *ast.CompositeLit {
	var lit *ast.CompositeLit
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			if id, ok := l.(*ast.Ident); ok && g.p.info.Defs[id] == obj {
				lit, _ = ast.Unparen(as.Rhs[i]).(*ast.CompositeLit)
			}
		}
		return true
	})
	return lit
}

// ts is the TypeScript type of a Go value encoded by encoding/json.
func (g *gen) ts(t types.Type) string {
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		switch o := n.Obj(); {
		case o.Pkg() != nil && o.Pkg().Path() == "time" && o.Name() == "Time":
			return "string"
		case o.Pkg() != nil && o.Pkg().Path() == "encoding/json" && o.Name() == "RawMessage":
			return "any"
		case hasMethod(n, "MarshalJSON"):
			return "any"
		case hasMethod(n, "MarshalText"):
			return "string"
		}
		if st, ok := n.Underlying().(*types.Struct); ok {
			return g.named(n, st)
		}
		return g.ts(n.Underlying())
	}
	switch t := t.(type) {
	case *types.Basic:
		switch {
		case t.Info()&types.IsBoolean != 0:
			return "boolean"
		case t.Info()&types.IsNumeric != 0:
			return "number"
		case t.Info()&types.IsString != 0:
			return "string"
		case t.Kind() == types.UntypedNil:
			return "null"
		}
	case *types.Pointer:
		return g.ts(t.Elem()) + " | null"
	case *types.Slice:
		if b, ok := t.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return "string"
		}
		return arrayOf(g.ts(t.Elem()))
	case *types.Array:
		return arrayOf(g.ts(t.Elem()))
	case *types.Map:
		return "Record<string, " + g.ts(t.Elem()) + ">"
	case *types.Struct:
		return inline(g.fields(t, map[*types.Struct]bool{}))
	case *types.Interface:
		return "any"
	}
	return "any"
}

func arrayOf(ts string) string {
	if strings.ContainsAny(ts, " |") && !strings.HasPrefix(ts, "{") && !strings.HasPrefix(ts, "Record<") {
		return "(" + ts + ")[]"
	}
	return ts + "[]"
}

// named declares an interface for a named struct (once) and returns its name.
func (g *gen) named(n *types.Named, st *types.Struct) string {
	o := n.Obj()
	if name, ok := g.names[o]; ok {
		return name
	}
	name := o.Name()
	if g.taken[name] && o.Pkg() != nil {
		name = upperFirst(o.Pkg().Name()) + name
	}
	for i := 2; g.taken[name]; i++ {
		name = fmt.Sprintf("%s%d", o.Name(), i)
	}
	g.taken[name] = true
	g.names[o] = name
	body := g.object(st) // after naming, so recursive types refer to the name
	where := o.Name()
	if o.Pkg() != nil {
		where = o.Pkg().Name() + "." + where
	}
	g.decls[name] = fmt.Sprintf("// %s\nexport interface %s %s\n", where, name, body)
	g.order = append(g.order, name)
	return name
}

// object is a struct's fields as encoding/json encodes them: json tag names,
// omitempty/omitzero as optional, embedded structs' fields promoted.
func (g *gen) object(st *types.Struct) string {
	fields := g.fields(st, map[*types.Struct]bool{})
	if len(fields) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, f := range fields {
		fmt.Fprintf(&b, "  %s%s: %s;\n", tsKey(f.name), optMark(f.opt), strings.Join(f.alts, " | "))
	}
	b.WriteString("}")
	return b.String()
}

func (g *gen) fields(st *types.Struct, seen map[*types.Struct]bool) []tsField {
	if seen[st] {
		return nil
	}
	seen[st] = true
	var out, promoted []tsField
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		tag := reflect.StructTag(st.Tag(i)).Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous() && name == "" {
			ft := types.Unalias(f.Type())
			if p, ok := ft.(*types.Pointer); ok {
				ft = types.Unalias(p.Elem())
			}
			if est, ok := ft.Underlying().(*types.Struct); ok {
				promoted = append(promoted, g.fields(est, seen)...)
				continue
			}
		}
		if !f.Exported() {
			continue
		}
		if name == "" {
			name = f.Name()
		}
		ts := g.ts(f.Type())
		if hasOpt(opts, "string") {
			ts = "string"
		}
		out = append(out, tsField{name: name, alts: []string{ts}, opt: hasOpt(opts, "omitempty") || hasOpt(opts, "omitzero")})
	}
	// Fields of embedded structs are promoted unless the outer struct has
	// one with the same name.
	for _, p := range promoted {
		if !slices.ContainsFunc(out, func(f tsField) bool { return f.name == p.name }) {
			out = append(out, p)
		}
	}
	return out
}

func (g *gen) render(routes []route) []byte {
	var b bytes.Buffer
	b.WriteString(`// Code generated by go run ./cmd/lex-apitypes; DO NOT EDIT.
//
// The JSON API's routes and the types they send and accept, read from
// internal/api. Regenerate after changing them: go test fails until then.

`)
	names := slices.Clone(g.order)
	sort.Strings(names)
	for _, n := range names {
		b.WriteString(g.decls[n])
		b.WriteString("\n")
	}
	// The most specific path first: ${string} also matches slashes, so
	// /api/items/${string} would otherwise match /api/items/1/children.
	sort.SliceStable(routes, func(i, j int) bool {
		si, sj := strings.Count(routes[i].path, "/"), strings.Count(routes[j].path, "/")
		if si != sj {
			return si > sj
		}
		wi, wj := strings.Count(routes[i].path, "{"), strings.Count(routes[j].path, "{")
		if wi != wj {
			return wi < wj
		}
		if routes[i].path != routes[j].path {
			return routes[i].path < routes[j].path
		}
		return routes[i].method < routes[j].method
	})
	b.WriteString("// [method, path, response, request body]; any where the handler doesn't\n// send or read JSON.\nexport type ApiRoutes = [\n")
	for _, r := range routes {
		method := strconv.Quote(r.method)
		if r.method == "" {
			method = "string"
		}
		resp := r.resp
		if r.shapes != nil {
			resp = append([]string{inline(merge(r.shapes))}, resp...)
		}
		fmt.Fprintf(&b, "  [%s, `%s`, %s, %s],\n", method, tsPath(r.path), union(resp), union(r.body))
	}
	b.WriteString("];\n")
	return b.Bytes()
}

// tsPath turns a ServeMux pattern into a template literal type.
func tsPath(p string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(p, '{')
		if i < 0 {
			b.WriteString(p)
			return b.String()
		}
		j := strings.IndexByte(p[i:], '}')
		b.WriteString(p[:i])
		if p[i:i+j+1] != "{$}" {
			b.WriteString("${string}")
		}
		p = p[i+j+1:]
	}
}

func union(ts []string) string {
	if len(ts) == 0 {
		return "any"
	}
	return strings.Join(ts, " | ")
}

func appendNew(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

func isNamed(t types.Type, pkgPath, name string) bool {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkgPath && n.Obj().Name() == name
}

func hasMethod(n *types.Named, name string) bool {
	obj, _, _ := types.LookupFieldOrMethod(types.NewPointer(n), true, n.Obj().Pkg(), name)
	_, ok := obj.(*types.Func)
	return ok
}

func hasOpt(opts, want string) bool {
	for o := range strings.SplitSeq(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func tsKey(k string) string {
	if k == "" {
		return `""`
	}
	for i, r := range k {
		if !(r == '_' || r == '$' || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || i > 0 && '0' <= r && r <= '9') {
			return strconv.Quote(k)
		}
	}
	return k
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
