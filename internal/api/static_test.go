package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"lex/internal/logx"
	"lex/internal/store"
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
}
