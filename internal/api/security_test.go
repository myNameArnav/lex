package api

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"lex/internal/logx"
	"lex/internal/meta"
	"lex/internal/store"
)

func securityServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{St: st, Log: logx.New(10, false), Web: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return s, h
}

func TestAPIAuthenticationAndCSRF(t *testing.T) {
	s, h := securityServer(t)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	regular, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	viewerToken, _ := s.St.CreateToken(regular.ID, "Test", "127.0.0.1")
	for _, tc := range []struct {
		name, method, path, token, origin, fetchSite string
		want                                         int
	}{
		{name: "anonymous", method: "GET", path: "/api/me", want: 401},
		{name: "query token rejected", method: "GET", path: "/api/me?token=" + adminToken, want: 401},
		{name: "cookie authenticated", method: "GET", path: "/api/me", token: adminToken, want: 200},
		{name: "admin protected", method: "GET", path: "/api/admin/config", token: viewerToken, want: 403},
		{name: "cross site prefs", method: "PUT", path: "/api/me/prefs", token: adminToken, origin: "https://example.org", fetchSite: "cross-site", want: 403},
		{name: "cross origin without fetch metadata", method: "PUT", path: "/api/me/prefs", token: adminToken, origin: "https://example.org", want: 403},
		{name: "same origin prefs", method: "PUT", path: "/api/me/prefs", token: adminToken, origin: "http://lex.test", fetchSite: "same-origin", want: 200},
		{name: "cross site login", method: "POST", path: "/api/auth/login", origin: "https://example.org", fetchSite: "cross-site", want: 403},
		{name: "webhook get disabled", method: "GET", path: "/api/webhook/scan", want: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://lex.test"+tc.path, strings.NewReader(`{}`))
			if tc.token != "" {
				r.AddCookie(&http.Cookie{Name: "lex_token", Value: tc.token})
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	for _, header := range []string{"Authorization", "X-Lex-Token"} {
		r := httptest.NewRequest("GET", "http://lex.test/api/me", nil)
		token := adminToken
		if header == "Authorization" {
			token = "Bearer " + token
		}
		r.Header.Set(header, token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s returned %d", header, w.Code)
		}
	}
}

func TestProxyTrust(t *testing.T) {
	s, _ := securityServer(t)
	cfg := s.St.Config()
	if cfg.TrustProxy {
		t.Fatal("proxy trust must default off")
	}
	cfg.TrustProxy = true
	cfg.TrustedProxies = "127.0.0.1/32, ::1/128"
	if _, err := s.St.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		peer, want string
		secure     bool
	}{
		{"192.168.1.25:1234", "192.168.1.25", false},
		{"203.0.113.10:1234", "203.0.113.10", false},
		{"127.0.0.1:1234", "198.51.100.20", true},
		{"[::1]:1234", "198.51.100.20", true},
	} {
		r := httptest.NewRequest("GET", "http://lex.test/api/me", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", "198.51.100.20, 127.0.0.1")
		r.Header.Set("X-Forwarded-Proto", "https")
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("%s client=%s", tc.peer, got)
		}
		if got := s.isHTTPS(r); got != tc.secure {
			t.Errorf("%s secure=%v", tc.peer, got)
		}
	}
	r := httptest.NewRequest("GET", "https://lex.test", nil)
	r.TLS = &tls.ConnectionState{}
	if !s.isHTTPS(r) {
		t.Fatal("native TLS must be secure")
	}
}

func TestSetupAndCookie(t *testing.T) {
	_, h := securityServer(t)
	for i, want := range []int{200, 409} {
		r := httptest.NewRequest("POST", "https://lex.test/api/setup", strings.NewReader(`{"name":"admin","password":"test-password-123"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("setup %d got %d: %s", i, w.Code, w.Body.String())
		}
		if i == 0 {
			cookies := w.Result().Cookies()
			if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
				t.Fatalf("cookie=%v", cookies)
			}
		}
	}
}

func TestPrivatePathsStayAdminOnly(t *testing.T) {
	s, h := securityServer(t)
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")
	_, err = s.St.CreateLibrary("Movies", "movies", []string{"/srv/private/media"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/libraries", "/api/home"} {
		r := httptest.NewRequest("GET", "http://lex.test"+path, nil)
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), "/srv/private") {
			t.Fatalf("%s leaked: %s", path, w.Body.String())
		}
	}
	f := &store.File{ID: 1, Path: "/srv/private/media/movie.mkv", ProbeError: "open /srv/private/media/movie.mkv"}
	encoded, _ := json.Marshal(fileForUser(f, false))
	if bytes.Contains(encoded, []byte("/srv/private")) {
		t.Fatal("file path leaked")
	}
	if fileForUser(f, true).Path != f.Path || f.Path == "" {
		t.Fatal("admin or store file mutated")
	}
}

func TestJSONLimitsAndTrailingData(t *testing.T) {
	for _, body := range []string{`{} {}`, `{} trailing`, "{}" + strings.Repeat(" ", 1<<20)} {
		var v any
		if err := readJSON(httptest.NewRequest("POST", "/", strings.NewReader(body)), &v); err == nil {
			t.Fatal("accepted invalid or oversized JSON")
		}
	}
}

func TestLoginLimiterIsBounded(t *testing.T) {
	s, _ := securityServer(t)
	for i := 0; i < 1100; i++ {
		s.loginFailed(time.Unix(int64(i), 0).String())
	}
	if len(s.loginFail) > 1000 {
		t.Fatalf("limiter grew to %d", len(s.loginFail))
	}
	for i := 0; i < 10; i++ {
		s.loginFailed("127.0.0.1")
	}
	if s.loginAllowed("127.0.0.1") {
		t.Fatal("failed logins were not limited")
	}
}

func TestOversizedImageCannotBeResized(t *testing.T) {
	s, _ := securityServer(t)
	s.Images = &meta.ImageCache{Dir: t.TempDir()}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	binary.BigEndian.PutUint32(data[16:20], 50000)
	binary.BigEndian.PutUint32(data[20:24], 50000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	source := filepath.Join(t.TempDir(), "oversized.png")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resized(source, 320); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("oversized image: %v", err)
	}
}
