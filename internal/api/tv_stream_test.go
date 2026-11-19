package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		// The header carries the AAC config (Firefox plays no sound
		// without it): ffprobe reads the profile from it.
		out, err = exec.Command("ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=profile", "-of", "csv=p=0", mp4).Output()
		if err != nil || strings.TrimSpace(string(out)) != "LC" {
			t.Fatalf("%s: AAC profile %q (no config in the header?), %v", ch, out, err)
		}
	}
	// The stats ask what the stream holds.
	var info struct {
		Video, Audio, Host string
		Height             int
		Playing            bool
	}
	if code := getJSON(t, h, viewer, "/api/tv/"+news+"/stream/info?t=1", &info); code != 200 || info.Video != "h264" || info.Audio != "aac" || info.Height != 180 || info.Playing || !strings.HasPrefix(origin.URL, "http://"+info.Host) {
		t.Fatalf("info: %d %+v", code, info)
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

// Tuning in mid-stream to open-GOP video starts at a keyframe whose
// B-frames show before it and need the GOP before; macOS's decoder fails
// on them, so they're dropped.
func TestLiveStreamOpenGOP(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir := t.TempDir()
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=25:d=8", "-f", "lavfi", "-i", "sine=d=8",
		"-c:v", "libx264", "-x264-params", "open-gop=1:keyint=25:min-keyint=25:scenecut=0:bframes=3:repeat-headers=1", "-c:a", "aac",
		"-f", "hls", "-hls_time", "2", "-hls_playlist_type", "vod", filepath.Join(dir, "og.m3u8")).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	// Joining live: the playlist from the second segment on.
	pl, err := os.ReadFile(filepath.Join(dir, "og.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	mid := strings.Replace(string(pl), "#EXTINF:2.000000,\nog0.ts\n", "", 1)
	if mid == string(pl) {
		t.Fatalf("unexpected playlist:\n%s", pl)
	}
	if err := writeFile(filepath.Join(dir, "mid.m3u8"), []byte(mid)); err != nil {
		t.Fatal(err)
	}
	// The segment does start with frames before its keyframe.
	first := func(path string) []float64 {
		t.Helper()
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "packet=pts_time", "-of", "csv=p=0", "-read_intervals", "%+#6", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		var pts []float64
		for _, f := range strings.Fields(string(out)) {
			v, _ := strconv.ParseFloat(strings.Trim(f, ","), 64)
			pts = append(pts, v)
		}
		return pts
	}
	if p := first(filepath.Join(dir, "og1.ts")); len(p) < 2 || p[1] >= p[0] {
		t.Fatalf("the test segment has no leading frames: %v", p)
	}
	origin := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer origin.Close()

	s, h := securityServer(t)
	s.FF.Path, s.FF.Probe = "ffmpeg", "ffprobe"
	viewer := tvLibrary(t, s)
	admin, _ := s.St.CreateUser("boss", "test-password-123", true)
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	w := adminRequest(t, h, token, "POST", "/api/admin/tv/streams", `[{"name":"Open","url":"`+origin.URL+`/mid.m3u8"}]`)
	var l tvLineup
	if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != 200 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "http://lex.test/api/tv/"+l.Saved+"/stream", nil)
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: viewer})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	mp4 := filepath.Join(dir, "out.mp4")
	if err := writeFile(mp4, rec.Body.Bytes()); rec.Code != 200 || err != nil {
		t.Fatalf("stream: %d %v", rec.Code, err)
	}
	p := first(mp4)
	for _, v := range p[1:] {
		if v < p[0] {
			t.Fatalf("a frame before the first keyframe was sent: %v", p)
		}
	}
}
