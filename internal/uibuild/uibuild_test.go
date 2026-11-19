package uibuild

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMinifyWritesSmallerFilesWithSourceMaps(t *testing.T) {
	dir := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appSrc := "import { helper } from './ui.js';\n// a comment\nexport function greet(name) {\n  const message = 'hi ' + name;\n  return helper(message);\n}\n"
	write("js/app.js", appSrc+"export { other } from './other.js';\nconst lazy = () => import('./admin.js');\n// import x from './comment.js'\n")
	write("js/ui.js", "export const helper = (s) => s;\n")
	write("css/app.css", "/* theme */\nbody {\n  color: #ffffff;\n  margin: 0px;\n}\n\n.card  >  .title {\n  font-weight: bold;\n  padding: 0px 0px 0px 0px;\n}\n")
	write("vendor/x/lib.js", "keep   this")
	write("sw.js", "const BUILD = /*LEX_BUILD*/null;")

	files, err := Minify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("minified %d files, want js/app.js, js/ui.js and css/app.css", len(files))
	}
	for _, f := range files {
		// A one-line module can grow by its source map comment.
		if f.After >= f.Before && filepath.Base(f.Path) != "ui.js" {
			t.Errorf("%s did not shrink: %d -> %d", f.Path, f.Before, f.After)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(filepath.Join(dir, p)); return string(b) }
	js := read("js/app.js")
	if strings.Contains(js, "a comment") || !strings.Contains(js, `from"./ui.js"`) || !strings.HasSuffix(js, "//# sourceMappingURL=app.js.map\n") {
		t.Errorf("js/app.js:\n%s", js)
	}
	if css := read("css/app.css"); !strings.HasSuffix(css, "/*# sourceMappingURL=app.css.map */\n") || strings.Contains(css, "theme") {
		t.Errorf("css/app.css:\n%s", css)
	}
	var m struct {
		Sources        []string `json:"sources"`
		SourcesContent []string `json:"sourcesContent"`
	}
	if err := json.Unmarshal([]byte(read("js/app.js.map")), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Sources) != 1 || m.Sources[0] != "src/app.js" || len(m.SourcesContent) != 1 || !strings.HasPrefix(m.SourcesContent[0], appSrc) {
		t.Errorf("source map: sources %v, content %q", m.Sources, m.SourcesContent)
	}
	if read("vendor/x/lib.js") != "keep   this" || read("sw.js") != "const BUILD = /*LEX_BUILD*/null;" {
		t.Error("vendor files and sw.js must be left as written")
	}
	var imports map[string][]string
	if err := json.Unmarshal([]byte(read(ImportsFile)), &imports); err != nil {
		t.Fatal(err)
	}
	// Static imports and re-exports, in order; not the dynamic import or
	// the commented-out one.
	if got := imports["app.js"]; !slices.Equal(got, []string{"./ui.js", "./other.js"}) || len(imports["ui.js"]) != 0 || len(imports) != 2 {
		t.Errorf("imports: %v", imports)
	}
	if _, err := Minify(dir); err == nil {
		t.Error("a second run over minified files must fail")
	}
}
