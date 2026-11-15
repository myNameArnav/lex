package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"lex/internal/logx"
	"lex/internal/store"
	"lex/web"
)

func TestStaticFilesAndAppShell(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{St: st, Log: logx.New(10, false), Web: fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<!doctype html>shell")},
		"icons/icon-192.png":      &fstest.MapFile{Data: []byte("\x89PNG\r\n\x1a\nicon")},
		"js/app.js":               &fstest.MapFile{Data: []byte("export {};")},
		"vendor/jassub/font.woff": &fstest.MapFile{Data: []byte("font")},
		"vendor/lexremux/x.wasm":  &fstest.MapFile{Data: make([]byte, 4096)},
	}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, method string
		code         int
		ctype, body  string
	}{
		{path: "/", code: 200, ctype: "text/html", body: "shell"},
		{path: "/settings/users", code: 200, ctype: "text/html", body: "shell"},
		{path: "/icons/icon-192.png", code: 200, ctype: "image/png"},
		{path: "/js/app.js", code: 200, ctype: "javascript"},
		// iOS probes these when there is no apple-touch-icon link; they must not get the shell.
		{path: "/apple-touch-icon.png", code: 404},
		{path: "/apple-touch-icon-precomposed.png", method: "HEAD", code: 404},
		{path: "/favicon.ico", code: 404},
		{path: "/js/missing.js", code: 404},
	} {
		method := tc.method
		if method == "" {
			method = "GET"
		}
		r := httptest.NewRequest(method, "http://lex.test"+tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("%s %s: got %d, want %d", method, tc.path, w.Code, tc.code)
			continue
		}
		if ct := w.Header().Get("Content-Type"); tc.ctype != "" && !strings.Contains(ct, tc.ctype) {
			t.Errorf("%s: content type %q, want %q", tc.path, ct, tc.ctype)
		}
		if tc.body != "" && !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s: body %q, want it to contain %q", tc.path, w.Body.String(), tc.body)
		}
		if tc.code == 404 && strings.Contains(w.Body.String(), "shell") {
			t.Errorf("%s: a missing file must not serve the app shell", tc.path)
		}
	}
	// WebAssembly (the remuxer, audio converter, subtitle renderer) is
	// several MB and compresses well.
	r := httptest.NewRequest("GET", "http://lex.test/vendor/lexremux/x.wasm", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Content-Type"), "application/wasm") || w.Body.Len() >= 4096 {
		t.Errorf("wasm: code %d, encoding %q, type %q, %d bytes; want gzipped application/wasm", w.Code, w.Header().Get("Content-Encoding"), w.Header().Get("Content-Type"), w.Body.Len())
	}
}

func swServer(t *testing.T, version string, web fstest.MapFS) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Version: version, St: st, Log: logx.New(10, false), Web: web}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// swBuild fetches /sw.js and returns the response and its stamped build.
func swBuild(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, struct {
	Version string   `json:"version"`
	Shell   []string `json:"shell"`
}) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "http://lex.test/sw.js", nil))
	if w.Code != 200 {
		t.Fatalf("/sw.js: got %d", w.Code)
	}
	var build struct {
		Version string   `json:"version"`
		Shell   []string `json:"shell"`
	}
	body := w.Body.String()
	i := strings.Index(body, "const BUILD = ")
	j := strings.Index(body[i:], " || ")
	if i < 0 || j < 0 {
		t.Fatalf("/sw.js: no BUILD line in %q", body)
	}
	if err := json.Unmarshal([]byte(body[i+len("const BUILD = "):i+j]), &build); err != nil {
		t.Fatalf("/sw.js: build is not stamped: %v", err)
	}
	return w, build
}

func TestServiceWorker(t *testing.T) {
	files := func(css string) fstest.MapFS {
		return fstest.MapFS{
			"index.html":             &fstest.MapFile{Data: []byte("<!doctype html>shell")},
			"offline.html":           &fstest.MapFile{Data: []byte("<!doctype html>offline")},
			"sw.js":                  &fstest.MapFile{Data: []byte("const BUILD = /*LEX_BUILD*/null || {};")},
			"css/app.css":            &fstest.MapFile{Data: []byte(css)},
			"js/app.js":              &fstest.MapFile{Data: []byte("export {};")},
			"icons/icon-192.png":     &fstest.MapFile{Data: []byte("\x89PNG")},
			"manifest.json":          &fstest.MapFile{Data: []byte("{}")},
			"vendor/lexremux/x.wasm": &fstest.MapFile{Data: []byte("\x00asm")},
		}
	}
	h := swServer(t, "1.0.0", files("body{}"))
	w, build := swBuild(t, h)

	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("content type %q, want text/javascript", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache so updates are found", cc)
	}
	// worker-src falls back to script-src: a same-origin worker must be allowed.
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "worker-src") && !strings.Contains(csp, "worker-src 'self'") {
		t.Errorf("CSP %q does not allow a same-origin service worker", csp)
	}
	want := []string{"/", "/css/app.css", "/icons/icon-192.png", "/js/app.js", "/manifest.json", "/offline.html"}
	if strings.Join(build.Shell, ",") != strings.Join(want, ",") {
		t.Errorf("shell %v, want %v", build.Shell, want)
	}
	if build.Version == "" {
		t.Error("empty version")
	}

	// The version follows the build: a changed shell file or server version
	// gives a new one, the same files give the same one.
	if _, b := swBuild(t, swServer(t, "1.0.0", files("body{}"))); b.Version != build.Version {
		t.Errorf("same build, version %q != %q", b.Version, build.Version)
	}
	if _, b := swBuild(t, swServer(t, "1.0.0", files("body{color:red}"))); b.Version == build.Version {
		t.Error("changed css kept the same version")
	}
	if _, b := swBuild(t, swServer(t, "1.0.1", files("body{}"))); b.Version == build.Version {
		t.Error("new server version kept the same version")
	}
}

// The real embedded sw.js carries the marker, and its shell covers the files
// the offline page needs.
func TestEmbeddedServiceWorker(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Version: "test", St: st, Log: logx.New(10, false), Web: web.FS()}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	_, build := swBuild(t, h)
	for _, p := range []string{"/", "/offline.html", "/js/offline.js", "/css/app.css", "/js/app.js", "/favicon.svg", "/manifest.json"} {
		if !slices.Contains(build.Shell, p) {
			t.Errorf("shell is missing %s", p)
		}
	}
	for _, p := range build.Shell {
		if strings.HasPrefix(p, "/vendor/") || strings.HasPrefix(p, "/api/") || p == "/sw.js" {
			t.Errorf("shell must not include %s", p)
		}
	}
}
