package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
