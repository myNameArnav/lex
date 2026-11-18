// Package uibuild minifies the browser UI at build time. The release image
// runs it over web/static before go build embeds the files, so the server
// serves smaller scripts and stylesheets with no runtime cost; a plain go
// build keeps the source as written.
package uibuild

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
func Minify(dir string) ([]File, error) {
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
