// Package uibuild minifies the browser UI at build time. The release image
// runs it over web/static before go build embeds the files, so the server
// serves smaller scripts and stylesheets with no runtime cost; a plain go
// build keeps the source as written.
package uibuild

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

// File is one minified file's size before and after.
type File struct {
	Path          string
	Before, After int
}

// Minify rewrites dir/js/*.js and dir/css/*.css in place, minified, each
// with a source map beside it (name.js.map, with the original source inside)
// and a comment pointing at it. Every module stays its own file: no
// bundling, so workers, import.meta.url and dynamic imports are untouched.
// Vendor files are already minified and are left alone. It refuses a
// directory it has already minified.
//
// The server preloads each page's module graph, which it reads from the
// import statements of the source. Minified modules are one line, so Minify
// first writes the graph out instead: js/imports.json, each module's static
// imports as esbuild parses them.
func Minify(dir string) ([]File, error) {
	if err := writeImports(dir); err != nil {
		return nil, err
	}
	var out []File
	for _, kind := range []struct {
		glob   string
		loader api.Loader
	}{{"js/*.js", api.LoaderJS}, {"css/*.css", api.LoaderCSS}} {
		paths, err := filepath.Glob(filepath.Join(dir, kind.glob))
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			f, err := minifyFile(p, kind.loader)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no js/*.js or css/*.css files", dir)
	}
	return out, nil
}

func minifyFile(p string, loader api.Loader) (File, error) {
	if _, err := os.Stat(p + ".map"); err == nil {
		return File{}, fmt.Errorf("%s: already minified (%s.map exists)", p, filepath.Base(p))
	}
	src, err := os.ReadFile(p)
	if err != nil {
		return File{}, err
	}
	base := filepath.Base(p)
	res := api.Transform(string(src), api.TransformOptions{
		Loader:            loader,
		Target:            api.ESNext, // minify only: no syntax lowering
		MinifyWhitespace:  true,
		MinifySyntax:      true,
		MinifyIdentifiers: true,
		LegalComments:     api.LegalCommentsNone,
		Sourcemap:         api.SourceMapExternal,
		// DevTools lists the original under src/, beside the minified file.
		Sourcefile: "src/" + base,
	})
	if len(res.Errors) > 0 {
		e := res.Errors[0]
		if e.Location != nil {
			return File{}, fmt.Errorf("%s:%d: %s", p, e.Location.Line, e.Text)
		}
		return File{}, errors.New(p + ": " + e.Text)
	}
	code := res.Code
	if loader == api.LoaderCSS {
		code = fmt.Appendf(code, "/*# sourceMappingURL=%s.map */\n", base)
	} else {
		code = fmt.Appendf(code, "//# sourceMappingURL=%s.map\n", base)
	}
	if err := os.WriteFile(p+".map", res.Map, 0o644); err != nil {
		return File{}, err
	}
	if err := os.WriteFile(p, code, 0o644); err != nil {
		return File{}, err
	}
	return File{Path: p, Before: len(src), After: len(code)}, nil
}

// ImportsFile is the import manifest Minify writes, relative to the UI dir:
// {"app.js": ["./theme.js", "./ui.js", ...], ...} for every js/*.js module,
// listing what it imports or re-exports from statically, in source order.
// Dynamic import() calls are left out: those load on demand.
const ImportsFile = "js/imports.json"

func writeImports(dir string) error {
	jsDir, err := filepath.Abs(filepath.Join(dir, "js"))
	if err != nil {
		return err
	}
	entries, err := filepath.Glob(filepath.Join(jsDir, "*.js"))
	if err != nil {
		return err
	}
	// Bundle mode makes esbuild resolve each import, which the plugin
	// records (static ones only) and marks external, so nothing is bundled
	// and nothing written. Importers are matched by file name: they are all
	// in js/, and esbuild may report them with symlinks resolved.
	var mu sync.Mutex
	static := map[[2]string]bool{} // importer's file name, specifier
	record := api.Plugin{Name: "record-imports", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: ".*"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.Kind == api.ResolveEntryPoint {
				return api.OnResolveResult{}, nil
			}
			if a.Kind == api.ResolveJSImportStatement {
				mu.Lock()
				static[[2]string{filepath.Base(a.Importer), a.Path}] = true
				mu.Unlock()
			}
			return api.OnResolveResult{Path: a.Path, External: true}, nil
		})
	}}
	res := api.Build(api.BuildOptions{
		EntryPoints:   entries,
		AbsWorkingDir: jsDir,
		Bundle:        true,
		Write:         false,
		Metafile:      true,
		Format:        api.FormatESModule,
		Outdir:        filepath.Join(jsDir, "out"),
		Plugins:       []api.Plugin{record},
		LogLevel:      api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return fmt.Errorf("reading imports: %s", res.Errors[0].Text)
	}
	// The metafile keeps each module's imports in source order.
	var meta struct {
		Inputs map[string]struct {
			Imports []struct {
				Path string `json:"path"`
			} `json:"imports"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(res.Metafile), &meta); err != nil {
		return err
	}
	graph := map[string][]string{}
	for _, entry := range entries {
		name := filepath.Base(entry)
		in, ok := meta.Inputs[name]
		if !ok {
			return fmt.Errorf("reading imports: %s missing from esbuild's metafile", name)
		}
		specs := []string{}
		for _, imp := range in.Imports {
			if static[[2]string{name, imp.Path}] && !slices.Contains(specs, imp.Path) {
				specs = append(specs, imp.Path)
			}
		}
		graph[name] = specs
	}
	b, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ImportsFile), append(b, '\n'), 0o644)
}
