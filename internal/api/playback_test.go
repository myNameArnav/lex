package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"lex/internal/cache"
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

// An admin Stop also ends a Direct Play response that is already open, so
// the browser can't keep playing what the connection still delivers.
func TestKillCutsOpenDirectResponse(t *testing.T) {
	s, _ := securityServer(t)
	s.Sess = stream.NewManager(s.St, s.Log)
	s.Cache = cache.New(s.St, s.Log, t.TempDir())
	u, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	const size = 256 << 20
	path := filepath.Join(t.TempDir(), "big.mp4")
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	fh.Truncate(size)
	fh.Close()
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{filepath.Dir(path)})
	itemID, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Big", Path: path})
	fileID, err := s.St.InsertFile(&store.File{ItemID: itemID, LibraryID: lib.ID, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sess.Open(&stream.Session{ID: "sess1", UserID: u.ID, ItemID: itemID, FileID: fileID}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", strconv.FormatInt(fileID, 10))
		s.direct(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/?sid=sess1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err := io.ReadFull(res.Body, make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if !s.Sess.Kill("sess1") {
		t.Fatal("kill: no session")
	}
	n, err := io.Copy(io.Discard, res.Body)
	if err == nil || n+1<<20 >= size {
		t.Fatalf("response kept going after the stop: read %d more bytes, err %v", n, err)
	}
}

// After a server restart the player may have the rest of the file buffered
// and make no media request: its heartbeat re-opens the session, so
// progress is still saved.
func TestHeartbeatReopensSessionAfterRestart(t *testing.T) {
	s, _ := securityServer(t)
	s.Sess = stream.NewManager(s.St, s.Log)
	u, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{"/m"})
	itemID, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Path: "/m/Heat.mkv"})
	fileID, err := s.St.InsertFile(&store.File{ItemID: itemID, LibraryID: lib.ID, Path: "/m/Heat.mkv", Duration: 6000})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body := `{"sessionId":"sess1","fileId":` + strconv.FormatInt(fileID, 10) + `,"method":"direct","position":120}`
	r := httptest.NewRequest("POST", "/api/playback/progress", strings.NewReader(body))
	s.progress(rec, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	if rec.Code != 200 {
		t.Fatalf("heartbeat for a session the server forgot: %d %s", rec.Code, rec.Body)
	}
	if sess := s.Sess.Get("sess1"); sess == nil || sess.ItemID != itemID || sess.Position != 120 {
		t.Fatalf("session not re-opened: %+v", sess)
	}
	// Without a file id (an old client) it is still a 404.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "/api/playback/progress", strings.NewReader(`{"sessionId":"other","position":1}`))
	s.progress(rec, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	if rec.Code != 404 {
		t.Fatalf("unknown session without a file: %d", rec.Code)
	}
}
