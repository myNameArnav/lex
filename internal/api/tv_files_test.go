package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A playlist can be uploaded rather than linked, with its guide; an
// address that is one channel's stream is refused as a playlist, saying so.
func TestTVPlaylistFiles(t *testing.T) {
	s, h := securityServer(t)
	s.TV.Dir = filepath.Join(t.TempDir(), "livetv")
	tvLibrary(t, s)
	admin, _ := s.St.CreateUser("boss", "test-password-123", true)
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	upload := func(kind, body string) (int, map[string]any) {
		t.Helper()
		w := adminRequest(t, h, token, "POST", "/api/admin/tv/files?kind="+kind, body)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	m3u := "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"478\" group-title=\"Entertainment\",Star\n#EXTVLCOPT:http-user-agent=App/1\nhttps://cdn.test/live/478/master.m3u8\n" +
		"#EXTINF:-1 tvg-id=\"481\" group-title=\"News\",Zee News\n#EXTVLCOPT:http-user-agent=App/1\nhttps://cdn.test/live/481/master.m3u8\n"
	code, pl := upload("playlist", m3u)
	if code != 200 || pl["channels"] != 2.0 {
		t.Fatalf("upload playlist: %d %v", code, pl)
	}
	hls := "#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-STREAM-INF:BANDWIDTH=280605\n478_0.m3u8\n"
	if code, out := upload("playlist", hls); code != 400 || !strings.Contains(out["error"].(string), "single stream") {
		t.Fatalf("an HLS stream as a playlist: %d %v", code, out)
	}
	if code, _ := upload("guide", "not xml at all"); code != 400 {
		t.Fatalf("a guide that isn't XMLTV: %d", code)
	}
	code, gd := upload("guide", `<?xml version="1.0"?><tv><programme start="20261119100000 +0000" stop="20261119110000 +0000" channel="478"><title>Show</title></programme></tv>`)
	if code != 200 {
		t.Fatalf("upload guide: %d %v", code, gd)
	}
	if n, _ := os.ReadDir(s.TV.Dir); len(n) != 2 {
		t.Fatalf("files kept: %d, want the playlist and guide (refused ones removed)", len(n))
	}
	w := adminRequest(t, h, token, "PUT", "/api/admin/tv/sources", `[{"name":"Esto","playlistFile":"`+pl["file"].(string)+`","guideFile":"`+gd["file"].(string)+`"}]`)
	var l tvLineup
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != 200 || len(l.Sources) != 1 || l.Sources[0].PlaylistFile == "" {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if w := adminRequest(t, h, token, "PUT", "/api/admin/tv/sources", `[{"name":"Bad","playlistFile":"../../etc/passwd"}]`); w.Code != 400 {
		t.Fatalf("a path as the file: %d", w.Code)
	}
	// Browsing finds both, and "all" answers every match.
	var found struct {
		Total    int `json:"total"`
		Channels []struct {
			Name      string `json:"name"`
			UserAgent string `json:"userAgent"`
		} `json:"channels"`
		Groups []string `json:"groups"`
	}
	r := httptest.NewRequest("GET", "http://lex.test/api/admin/tv/sources/"+l.Sources[0].ID+"/channels?all=1", nil)
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil || found.Total != 2 || len(found.Channels) != 2 || found.Channels[0].UserAgent != "App/1" || len(found.Groups) != 2 {
		t.Fatalf("browse: %d %s", rec.Code, rec.Body.String())
	}

	// A playlist address that is a stream is refused when saved.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(hls)) }))
	defer origin.Close()
	w = adminRequest(t, h, token, "PUT", "/api/admin/tv/sources", `[{"name":"TV","url":"`+origin.URL+`/live/478/master.m3u8"}]`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "single stream") {
		t.Fatalf("stream as playlist: %d %s", w.Code, w.Body.String())
	}
}

// Automatic channels can be taken out of the lineup and restored.
func TestTVRemoveAutomatic(t *testing.T) {
	s, h := securityServer(t)
	tvLibrary(t, s)
	admin, _ := s.St.CreateUser("boss", "test-password-123", true)
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	var l tvLineup
	w := adminRequest(t, h, token, "PUT", "/api/admin/tv", `{"removed":["lib-1"]}`)
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || len(l.Channels) != 0 || len(l.Removed) != 1 || l.Removed[0].Name != "Everything" {
		t.Fatalf("removed: %d %s", w.Code, w.Body.String())
	}
	if code := getJSON(t, h, token, "/api/tv/lib-1", nil); code != 404 {
		t.Fatalf("tuning a removed channel: %d", code)
	}
	w = adminRequest(t, h, token, "PUT", "/api/admin/tv", `{"removed":[]}`)
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || len(l.Channels) != 1 || len(l.Removed) != 0 {
		t.Fatalf("restored: %d %s", w.Code, w.Body.String())
	}
}
