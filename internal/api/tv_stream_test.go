package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A live channel plays through the server: it fetches the stream with the
// channel's User-Agent (the provider refuses others) and sends fragmented
// MP4, converting video that isn't H.264.
func TestLiveStreamChannel(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	src := []string{"-f", "lavfi", "-i", "testsrc2=s=320x180:r=25:d=6", "-f", "lavfi", "-i", "sine=d=6"}
	run(append(src, "-c:v", "libx264", "-g", "25", "-c:a", "aac", "-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", filepath.Join(dir, "h264.m3u8"))...)
	run(append(src, "-c:v", "mpeg2video", "-c:a", "mp2", "-f", "mpegts", filepath.Join(dir, "mpeg2.ts"))...)
	var hits, refused int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "TestPlayer/1.0" || r.Header.Get("X-Token") != "secret" {
			refused++
			http.Error(w, "no", 401)
			return
		}
		hits++
		http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
	}))
	defer origin.Close()

	s, h := securityServer(t)
	s.FF.Path, s.FF.Probe = "ffmpeg", "ffprobe"
	viewer := tvLibrary(t, s)
	admin, _ := s.St.CreateUser("boss", "test-password-123", true)
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")

	// The probe says what's in it; without the User-Agent it fails.
	w := adminRequest(t, h, token, "POST", "/api/admin/tv/streams/probe", `{"url":"`+origin.URL+`/h264.m3u8"}`)
	if w.Code != 502 {
		t.Fatalf("probe without the User-Agent: %d %s", w.Code, w.Body.String())
	}
	if w = adminRequest(t, h, token, "POST", "/api/admin/tv/streams/probe", `{"url":"`+origin.URL+`/h264.m3u8","userAgent":"TestPlayer/1.0"}`); w.Code != 502 {
		t.Fatalf("probe without the token header: %d", w.Code)
	}
	w = adminRequest(t, h, token, "POST", "/api/admin/tv/streams/probe", `{"url":"`+origin.URL+`/h264.m3u8","userAgent":"TestPlayer/1.0","headers":[{"name":"X-Token","value":"secret"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"video":"h264"`) || !strings.Contains(w.Body.String(), `"convert":false`) {
		t.Fatalf("probe: %d %s", w.Code, w.Body.String())
	}
	if w = adminRequest(t, h, token, "POST", "/api/admin/tv/streams", `[{"name":"Local","url":"file:///etc/passwd"}]`); w.Code != 400 {
		t.Fatalf("a local file: %d", w.Code)
	}
	w = adminRequest(t, h, token, "POST", "/api/admin/tv/streams", `[{"name":"News","url":"`+origin.URL+`/h264.m3u8","userAgent":"TestPlayer/1.0","headers":[{"name":"X-Token","value":"secret"}]},{"name":"Old","url":"`+origin.URL+`/mpeg2.ts","headers":[{"name":"User-Agent","value":"TestPlayer/1.0"},{"name":"X-Token","value":"secret"}]}]`)
	var l tvLineup
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != 200 || len(l.Channels) != 3 || l.Channels[1].Source != "stream" || l.Channels[1].Stream.UserAgent != "TestPlayer/1.0" {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	news := l.Saved

	// Viewers see it in the guide, as one block without a guide, and tune in.
	var guide struct {
		Channels []channelView `json:"channels"`
	}
	if code := getJSON(t, h, viewer, "/api/tv", &guide); code != 200 || len(guide.Channels) != 3 || !guide.Channels[1].Live || len(guide.Channels[1].Programs) != 1 || guide.Channels[1].Programs[0].Item.Title != "News" {
		t.Fatalf("guide: %d %+v", code, guide)
	}
	var tuned struct {
		Stream struct{ URL string } `json:"stream"`
	}
	if code := getJSON(t, h, viewer, "/api/tv/"+news, &tuned); code != 200 || tuned.Stream.URL != "/api/tv/"+news+"/stream" {
		t.Fatalf("tune: %d %+v", code, tuned)
	}
	for _, ch := range []string{news, l.Channels[2].ID} {
		r := httptest.NewRequest("GET", "http://lex.test/api/tv/"+ch+"/stream", nil)
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: viewer})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		if rec.Code != 200 || rec.Header().Get("Content-Type") != "video/mp4" || len(body) < 1000 || string(body[4:8]) != "ftyp" {
			t.Fatalf("%s: %d %q %d bytes", ch, rec.Code, rec.Header().Get("Content-Type"), len(body))
		}
		mp4 := filepath.Join(dir, "out.mp4")
		if err := writeFile(mp4, body); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", mp4).Output()
		if err != nil || strings.Fields(string(out))[0] != "h264" || !strings.Contains(string(out), "aac") {
			t.Fatalf("%s sent %q, %v", ch, out, err)
		}
	}
	if hits == 0 {
		t.Fatal("the stream was never fetched")
	}
	// Viewers can't add channels, and turning Live TV off stops the stream.
	if w := adminRequest(t, h, viewer, "POST", "/api/admin/tv/streams", `[]`); w.Code != 403 {
		t.Fatalf("viewer: %d", w.Code)
	}
	c := s.St.Config()
	c.LiveTV = false
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, viewer, "/api/tv/"+news+"/stream", nil); code != 404 {
		t.Fatalf("stream while off: %d", code)
	}
}

func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }
