package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lex/internal/store"
)

// An admin on a server without libraries gets [] (not null), like viewers do.
func TestHomeWithoutLibrariesListsNoLibraries(t *testing.T) {
	s, handler := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{admin.ID, viewer.ID} {
		token, _ := s.St.CreateToken(uid, "Test", "127.0.0.1")
		for _, path := range []string{"/api/home", "/api/libraries"} {
			r := httptest.NewRequest("GET", "http://lex.test"+path, nil)
			r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			body := w.Body.String()
			if w.Code != 200 || strings.Contains(body, "null") {
				t.Fatalf("user %d %s: %d %s", uid, path, w.Code, body)
			}
		}
	}
}

// A library nested in another's folder stays empty (a file can only be in
// one library); the admin list says which library has its files.
func TestAdminLibrariesReportFilesInOtherLibraries(t *testing.T) {
	s, handler := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	movies, _ := s.St.CreateLibrary("Movies", "movies", []string{"/m"})
	kids, _ := s.St.CreateLibrary("Kids", "movies", []string{"/m/Kids"})
	other, _ := s.St.CreateLibrary("Shows", "shows", []string{"/tv"})
	for _, p := range []string{"/m/Heat (1995)/Heat.mkv", "/m/Kids/Up (2009)/Up.mkv", "/m/Kids/Cars (2006)/Cars.mkv", "/m/Kidsville.mkv"} {
		it, _ := s.St.InsertItem(&store.Item{LibraryID: movies.ID, Kind: "movie", Title: p, Path: p})
		if _, err := s.St.InsertFile(&store.File{ItemID: it, LibraryID: movies.ID, Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	r := httptest.NewRequest("GET", "http://lex.test/api/admin/libraries", nil)
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var libs []adminLibrary
	if err := json.Unmarshal(w.Body.Bytes(), &libs); err != nil {
		t.Fatal(err)
	}
	got := map[int64][]libraryShare{}
	for _, l := range libs {
		got[l.ID] = l.Elsewhere
	}
	if e := got[kids.ID]; len(e) != 1 || e[0] != (libraryShare{"Movies", 2}) {
		t.Fatalf("Kids: elsewhere = %+v, want 2 files in Movies", e)
	}
	if e := got[movies.ID]; len(e) != 0 {
		t.Fatalf("Movies: elsewhere = %+v, want none (it has the files)", e)
	}
	if e := got[other.ID]; len(e) != 0 {
		t.Fatalf("Shows: elsewhere = %+v, want none", e)
	}
}
