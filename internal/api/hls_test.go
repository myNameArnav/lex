package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"lex/internal/stream"
)

func TestHLSStreamKeyEndsWithLogin(t *testing.T) {
	s, _ := securityServer(t)
	s.Sess = stream.NewManager(s.St, s.Log)
	u, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(u.ID, "Test", "127.0.0.1")
	if _, err := s.Sess.Open(&stream.Session{ID: "sess1", UserID: u.ID, ItemID: 1, FileID: 1, AuthToken: token}); err != nil {
		t.Fatal(err)
	}
	key := s.Sess.StreamKey("sess1")
	h := s.hlsAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	get := func(url string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec.Code
	}
	if code := get("/api/files/1/hls/index.m3u8?sid=sess1&k=" + key); code != http.StatusNoContent {
		t.Fatalf("valid stream key: %d", code)
	}
	if code := get("/api/files/1/hls/index.m3u8?sid=sess1&k=wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong stream key: %d", code)
	}
	// A password change revokes the account's logins; the stream key that
	// came from one of them must stop working too.
	if err := s.St.SetPassword(u.ID, "another-password-456"); err != nil {
		t.Fatal(err)
	}
	if code := get("/api/files/1/hls/index.m3u8?sid=sess1&k=" + key); code != http.StatusUnauthorized {
		t.Fatalf("stream key after password change: %d, want 401", code)
	}
}
