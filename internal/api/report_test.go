package api

import (
	"strings"
	"testing"

	"lex/internal/store"
)

func TestReportHidesPersonalDetails(t *testing.T) {
	s, h := securityServer(t)
	s.DataDir = t.TempDir()
	admin, err := s.St.CreateInitialAdmin("alexandra", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	viewer, _ := s.St.CreateUser("viewer", "test-password-123", false)
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	viewerToken, _ := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")
	lib, _ := s.St.CreateLibrary("Films", "movies", []string{"/srv/films"})
	item, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Private Holiday Video", Path: "/srv/films/Private Holiday Video"})
	s.St.InsertFile(&store.File{ItemID: item, LibraryID: lib.ID, Path: "/srv/films/Private Holiday Video/holiday.2024.mkv", Size: 1, Mtime: 1})
	cfg := s.St.Config()
	cfg.RadarrKey, cfg.RadarrURL = "radarr-secret-key", "http://192.168.0.49:7878"
	s.St.SaveConfig(cfg)
	s.Log.Infof("play: alexandra — Private Holiday Video via direct (Firefox on macOS, 100.101.102.103)")
	s.Log.Warnf("open /srv/films/Private Holiday Video/holiday.2024.mkv: stalled, from 192.168.0.12")

	if w := adminRequest(t, h, viewerToken, "GET", "/api/admin/report", ""); w.Code != 403 {
		t.Fatalf("viewer: %d", w.Code)
	}
	w := adminRequest(t, h, token, "GET", "/api/admin/report", "")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "lex-report-") {
		t.Fatalf("report: %d %q", w.Code, w.Header())
	}
	for _, leak := range []string{"alexandra", "Private Holiday", "holiday.2024", "/srv/films", "100.101.102.103", "192.168.0", "radarr-secret-key", s.DataDir} {
		if strings.Contains(body, leak) {
			t.Errorf("report contains %q:\n%s", leak, body)
		}
	}
	for _, want := range []string{"play: <user 1> — <title 1> via direct", "<tailnet ip 1>", "open <file 1>.mkv: stalled, from <lan ip ", "radarrKey = (set, hidden)", "== Log =="} {
		if !strings.Contains(body, want) {
			t.Errorf("report lacks %q:\n%s", want, body)
		}
	}
}
