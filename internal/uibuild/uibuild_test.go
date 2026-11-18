package uibuild

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	write("js/app.js", appSrc)
	write("css/app.css", "/* theme */\nbody {\n  color: #ffffff;\n  margin: 0px;\n}\n\n.card  >  .title {\n  font-weight: bold;\n  padding: 0px 0px 0px 0px;\n}\n")
	write("vendor/x/lib.js", "keep   this")
	write("sw.js", "const BUILD = /*LEX_BUILD*/null;")

	files, err := Minify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("minified %d files, want js/app.js and css/app.css", len(files))
	}
	for _, f := range files {
		if f.After >= f.Before {
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
	if len(m.Sources) != 1 || m.Sources[0] != "src/app.js" || len(m.SourcesContent) != 1 || m.SourcesContent[0] != appSrc {
		t.Errorf("source map: sources %v, content %q", m.Sources, m.SourcesContent)
	}
	if read("vendor/x/lib.js") != "keep   this" || read("sw.js") != "const BUILD = /*LEX_BUILD*/null;" {
		t.Error("vendor files and sw.js must be left as written")
	}
	if _, err := Minify(dir); err == nil {
		t.Error("a second run over minified files must fail")
	}
}
