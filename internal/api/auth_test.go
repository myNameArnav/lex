package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeReportsCaps(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	viewerToken, _ := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")

	caps := func(t *testing.T, token string) map[string]bool {
		t.Helper()
		r := httptest.NewRequest("GET", "http://lex.test/api/me", nil)
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("GET /api/me: %d %s", w.Code, w.Body.String())
		}
		var body struct{ Caps map[string]bool }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Caps
	}
	want := func(t *testing.T, got map[string]bool, subs, cache bool) {
		t.Helper()
		if v, ok := got["subtitleSearch"]; !ok || v != subs {
			t.Errorf("subtitleSearch = %v (present %v), want %v", v, ok, subs)
		}
		if v, ok := got["cacheEnabled"]; !ok || v != cache {
			t.Errorf("cacheEnabled = %v (present %v), want %v", v, ok, cache)
		}
	}

	want(t, caps(t, adminToken), false, false)
	want(t, caps(t, viewerToken), false, false)

	cfg := s.St.Config()
	cfg.OpenSubtitlesKey = "  key  "
	cfg.CacheEnabled = true
	if _, err := s.St.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	want(t, caps(t, adminToken), true, true)
	// The cache actions are admin-only, so viewers never see it enabled.
	want(t, caps(t, viewerToken), true, false)

	cfg.OpenSubtitlesKey = "   "
	if _, err := s.St.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	want(t, caps(t, adminToken), false, true)

	// Signing in returns the caps too, so the UI needn't ask again.
	r := httptest.NewRequest("POST", "http://lex.test/api/auth/login", strings.NewReader(`{"name":"viewer","password":"test-password-123"}`))
	r.Header.Set("Origin", "http://lex.test")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var login struct{ Caps map[string]bool }
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	want(t, login.Caps, false, false)
}
