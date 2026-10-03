package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lex/internal/store"
	"lex/internal/stream"
)

// An admin's Stop must stick: the viewer's heartbeat and media requests get
// 410 with the reason instead of silently re-opening the session.
func TestKilledSessionAnswers410(t *testing.T) {
	s, _ := securityServer(t)
	s.Sess = stream.NewManager(s.St, s.Log)
	u, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sess.Open(&stream.Session{ID: "sess1", UserID: u.ID, ItemID: 1, FileID: 1}); err != nil {
		t.Fatal(err)
	}
	withUser := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), userKey, u))
	}
	beat := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.progress(rec, withUser(httptest.NewRequest("POST", "/api/playback/progress", strings.NewReader(`{"sessionId":"sess1","position":12}`))))
		return rec
	}
	if rec := beat(); rec.Code != 200 {
		t.Fatalf("heartbeat before stop: %d", rec.Code)
	}
	kill := httptest.NewRequest("DELETE", "/api/admin/sessions/sess1", nil)
	kill.SetPathValue("id", "sess1")
	rec := httptest.NewRecorder()
	s.killSession(rec, kill)
	if rec.Code != 200 {
		t.Fatalf("kill: %d", rec.Code)
	}
	if rec := beat(); rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "stopped by the server admin") {
		t.Fatalf("heartbeat after stop: %d %s", rec.Code, rec.Body)
	}
	_, err = s.sessionFor(withUser(httptest.NewRequest("GET", "/api/files/1/direct?sid=sess1", nil)), &store.File{ID: 1, ItemID: 1}, "sess1", "direct")
	if !errors.Is(err, stream.ErrStopped) {
		t.Fatalf("media request after stop: %v, want ErrStopped", err)
	}
	rec = httptest.NewRecorder()
	writeSessionErr(rec, err)
	if rec.Code != http.StatusGone {
		t.Fatalf("media request status: %d, want 410", rec.Code)
	}
	if n := len(s.Sess.Snapshot()); n != 0 {
		t.Fatalf("stopped session re-created: %d sessions", n)
	}
}
