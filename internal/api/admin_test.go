package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"lex/internal/store"
)

// adminRequest sends an authenticated same-origin JSON request.
func adminRequest(t *testing.T, h http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://lex.test"+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
	r.Header.Set("Origin", "http://lex.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func errMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return body.Error
}

func TestAdminCannotChangeOwnRole(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.St.CreateUser("other", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")

	w := adminRequest(t, h, token, "PUT", "/api/admin/users/"+itoa(admin.ID), `{"isAdmin":false}`)
	if w.Code != 400 || !strings.Contains(errMessage(t, w), "own role") {
		t.Fatalf("self-demotion: %d %s", w.Code, w.Body.String())
	}
	if u, _ := s.St.User(admin.ID); !u.IsAdmin {
		t.Fatal("self-demotion went through")
	}
	// A password change on your own row is still fine.
	if w := adminRequest(t, h, token, "PUT", "/api/admin/users/"+itoa(admin.ID), `{"password":"another-password-1"}`); w.Code != 200 {
		t.Fatalf("own password: %d %s", w.Code, w.Body.String())
	}
	token, _ = s.St.CreateToken(admin.ID, "Test", "127.0.0.1") // the password change signed us out
	// Demoting someone else works.
	if w := adminRequest(t, h, token, "PUT", "/api/admin/users/"+itoa(other.ID), `{"isAdmin":false}`); w.Code != 200 {
		t.Fatalf("demote other: %d %s", w.Code, w.Body.String())
	}
	if u, _ := s.St.User(other.ID); u.IsAdmin {
		t.Fatal("other admin was not demoted")
	}
}

func TestLibraryFolderValidation(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	root := t.TempDir()
	dir := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	movies, sub, shows := dir("movies"), dir("movies/Copper Sky (2019)"), dir("shows")
	lib, err := s.St.CreateLibrary("Movies", "movies", []string{movies})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.St.CreateLibrary("Shows", "shows", []string{shows})
	if err != nil {
		t.Fatal(err)
	}
	body := func(name, kind string, paths ...string) string {
		b, _ := json.Marshal(map[string]any{"name": name, "kind": kind, "paths": paths})
		return string(b)
	}
	for _, tc := range []struct {
		name, method, path, body, want string
	}{
		{"same folder as another library", "POST", "/api/admin/libraries", body("Copy", "movies", movies), "already in library \"Movies\""},
		{"same folder, trailing slash", "POST", "/api/admin/libraries", body("Copy", "movies", movies+"/"), "already in library"},
		{"parent and child together", "POST", "/api/admin/libraries", body("Both", "movies", movies, sub), "is inside"},
		{"child and parent together", "POST", "/api/admin/libraries", body("Both", "movies", sub, movies), "is inside"},
		{"listed twice", "POST", "/api/admin/libraries", body("Twice", "shows", shows+"/", shows), "listed twice"},
		{"no folders", "POST", "/api/admin/libraries", body("Empty", "movies"), "at least one folder"},
		{"blank name", "POST", "/api/admin/libraries", body("  ", "movies", sub), "enter a name"},
		{"bad kind", "POST", "/api/admin/libraries", body("X", "music", sub), "type must be"},
		{"edit onto another library's folder", "PUT", "/api/admin/libraries/" + itoa(other.ID), body("Shows", "shows", shows, movies), "already in library \"Movies\""},
		{"edit adds a sub-folder", "PUT", "/api/admin/libraries/" + itoa(lib.ID), body("Movies", "movies", movies, sub), "is inside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := adminRequest(t, h, token, tc.method, tc.path, tc.body)
			if w.Code != 400 || !strings.Contains(errMessage(t, w), tc.want) {
				t.Fatalf("got %d %s, want 400 containing %q", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	libs, err := s.St.Libraries()
	if err != nil || len(libs) != 2 {
		t.Fatalf("libraries changed: %v %v", libs, err)
	}
	for _, l := range libs {
		if len(l.Paths) != 1 {
			t.Fatalf("library %q paths changed: %v", l.Name, l.Paths)
		}
	}
}

func TestCheckLibraryPathsAllows(t *testing.T) {
	libs := []store.Library{{ID: 1, Name: "Movies", Paths: []string{"/media/movies"}}, {ID: 2, Name: "Root", Paths: []string{"/"}}}
	for _, tc := range []struct {
		paths []string
		self  int64
	}{
		{[]string{"/media/movies"}, 1},                      // editing keeps its own folder
		{[]string{"/media/movies/Copper Sky (2019)"}, 0},    // nesting across libraries is allowed
		{[]string{"/media"}, 0},                             // so is a parent of another library's folder
		{[]string{"/media/movies-4k", "/media/movies2"}, 0}, // a shared prefix isn't nesting
		{[]string{"/media/shows", "/srv/shows"}, 0},         // unrelated folders
		{[]string{"/media/movies", "/media/movies-old"}, 1}, // sibling with a common prefix
		{[]string{"/data"}, 2},                              // the root library edits itself
	} {
		if err := checkLibraryPaths(tc.paths, libs, tc.self); err != nil {
			t.Errorf("%v (self %d): %v", tc.paths, tc.self, err)
		}
	}
	if err := checkLibraryPaths([]string{"/", "/media"}, nil, 0); err == nil {
		t.Error("/ together with /media was allowed")
	}
}

func TestBrowseFSErrorsAreReadable(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	dir := t.TempDir()
	file := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		filepath.Join(dir, "missing"): "There's no folder at",
		file:                          "is a file, not a folder",
	} {
		w := adminRequest(t, h, token, "GET", "/api/admin/fs?path="+url.QueryEscape(p), "")
		if w.Code != 400 || !strings.Contains(errMessage(t, w), want) {
			t.Errorf("%s: %d %s, want %q", p, w.Code, w.Body.String(), want)
		}
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestDevicesMarkCurrentSession(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := s.St.CreateToken(admin.ID, "Firefox on macOS", "127.0.0.1")
	other, _ := s.St.CreateToken(admin.ID, "Safari on iPhone", "127.0.0.2")

	w := adminRequest(t, h, mine, "GET", "/api/admin/devices", "")
	if w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var devs []store.TokenInfo
	if err := json.Unmarshal(w.Body.Bytes(), &devs); err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices", len(devs))
	}
	for _, d := range devs {
		if want := d.Prefix == mine[:8]; d.Current != want {
			t.Fatalf("%s (%s): current=%v, want %v", d.Client, d.Prefix, d.Current, want)
		}
		if d.Prefix == other[:8] && d.Current {
			t.Fatal("another session was marked current")
		}
	}
}
