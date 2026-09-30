package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"lex/internal/store"
)

// Requests use the client without holding subMu, so a settings change must
// never modify a client that's already been handed out (run with -race).
func TestSubClientSettingsChangeDoesNotRace(t *testing.T) {
	s, _ := securityServer(t)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			c := s.St.Config()
			c.OpenSubtitlesKey = fmt.Sprintf("key-%d", i)
			c.OpenSubtitlesUser = fmt.Sprintf("user-%d", i)
			if _, err := s.St.SaveConfig(c); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for range 200 {
				c := s.subClient()
				_ = c.Key + c.User + c.Pass
			}
		})
	}
	wg.Wait()
	if c := s.subClient(); c.Key != "key-49" || c.User != "user-49" {
		t.Fatalf("client has %q/%q, want the latest settings", c.Key, c.User)
	}
	if s.subClient() != s.subClient() {
		t.Fatal("unchanged settings should keep the same client (and its login token)")
	}
}

func TestSubDeleteOnlyByDownloaderOrAdmin(t *testing.T) {
	s, h := securityServer(t)
	admin, _ := s.St.CreateInitialAdmin("admin", "test-password-123")
	owner, _ := s.St.CreateUser("owner", "test-password-123", false)
	other, _ := s.St.CreateUser("other", "test-password-123", false)
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`,
		`INSERT INTO items(id,library_id,kind,title,sort_title,added_at,updated_at) VALUES(1,1,'movie','Dune','dune',0,0)`,
		`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at) VALUES(1,1,1,'/m/dune.mkv',1,1,0)`,
	} {
		if _, err := s.St.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	add := func(name string, user int64) (*store.DownloadedSub, string) {
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644)
		d := &store.DownloadedSub{FileID: 1, Path: p, UserID: user}
		if _, err := s.St.AddDownloadedSub(d); err != nil {
			t.Fatal(err)
		}
		return d, p
	}
	del := func(d *store.DownloadedSub, u *store.User) int {
		tok, _ := s.St.CreateToken(u.ID, "Test", "127.0.0.1")
		r := httptest.NewRequest("DELETE", fmt.Sprintf("http://lex.test/api/files/1/subs/%d", store.DownloadedSubBase+d.ID), nil)
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: tok})
		r.Header.Set("Origin", "http://lex.test")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	mine, minePath := add("a.srt", owner.ID)
	if code := del(mine, other); code != 403 {
		t.Fatalf("other user delete = %d, want 403", code)
	}
	if _, err := os.Stat(minePath); err != nil {
		t.Fatal("refused delete removed the file")
	}
	if code := del(mine, owner); code != 200 {
		t.Fatalf("owner delete = %d", code)
	}
	if _, err := os.Stat(minePath); !os.IsNotExist(err) {
		t.Fatal("subtitle file kept after delete")
	}
	legacy, _ := add("b.srt", 0)
	if code := del(legacy, owner); code != 403 {
		t.Fatalf("delete of an unowned subtitle by a user = %d, want 403", code)
	}
	if code := del(legacy, admin); code != 200 {
		t.Fatalf("admin delete = %d", code)
	}
}
