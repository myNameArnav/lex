package api

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"lex/internal/cache"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/trickplay"
)

// A PGS track is drawn in the browser: the plan doesn't burn it in, and
// the track is served as .sup.
func TestPGSDrawnInBrowser(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	sup, err := filepath.Abs(filepath.Join("..", "stream", "testdata", "pgs.sup"))
	if err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "Movie.mkv")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=black:s=1920x1080:d=5", "-copyts", "-i", sup,
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:s", "copy", "-y", mkv).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg can't mux PGS here: %v %s", err, out)
	}
	s, h := securityServer(t)
	s.DataDir = dir
	s.Sess = stream.NewManager(s.St, s.Log)
	s.Cache = cache.New(s.St, s.Log, t.TempDir())
	s.HLS = stream.NewHLS(filepath.Join(dir, "hls"), "ffmpeg", s.Log)
	s.Trick = trickplay.New(s.St, s.Log, "ffmpeg", filepath.Join(dir, "trickplay"))
	s.Subs = stream.NewSubs(filepath.Join(dir, "subs"), "ffmpeg", "ffprobe", s.Log)
	os.MkdirAll(filepath.Join(dir, "subs"), 0o700)
	admin, _ := s.St.CreateInitialAdmin("admin", "test-password-123")
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{dir})
	itemID, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Movie", Path: mkv})
	fileID, err := s.St.InsertFile(&store.File{ItemID: itemID, LibraryID: lib.ID, Path: mkv, Size: 1, Mtime: 1})
	if err != nil {
		t.Fatal(err)
	}
	info := &store.MediaInfo{Duration: 5, Bitrate: 1_000_000, Streams: []store.Stream{
		{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, BitDepth: 8, CodecString: "avc1.640028", Default: true},
		{Index: 1, Type: "subtitle", Codec: "hdmv_pgs_subtitle"},
	}}
	if err := s.St.SaveProbe(fileID, info, ""); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(fileID, 10)

	body, _ := json.Marshal(map[string]any{"itemId": itemID, "fileId": fileID, "audio": -1, "subtitle": 1, "sessionId": "s1",
		"caps": map[string]any{"mse": true, "pgs": true, "direct": map[string]bool{"mkv": true}, "video": map[string]bool{"h264": true}, "nativeVideo": map[string]bool{"h264": true}}})
	w := adminRequest(t, h, token, "POST", "/api/playback/plan", string(body))
	if w.Code != 200 {
		t.Fatalf("plan: %d %s", w.Code, w.Body.String())
	}
	var res struct{ Plan stream.Plan }
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Plan.Method != "direct" || res.Plan.BurnSubs || res.Plan.DrawSubs != "pgs" {
		t.Fatalf("plan = %+v", res.Plan)
	}

	w = adminRequest(t, h, token, "GET", "/api/files/"+id+"/subs/1.sup", "")
	want, _ := os.ReadFile(sup)
	if w.Code != 200 || w.Header().Get("X-Lex-Partial") != "" || len(w.Body.Bytes()) != len(want) || !bytes.HasPrefix(w.Body.Bytes(), []byte("PG")) {
		t.Fatalf("sup: %d %q %d bytes", w.Code, w.Header(), w.Body.Len())
	}
	// Only PGS tracks are served this way.
	if w := adminRequest(t, h, token, "GET", "/api/files/"+id+"/subs/0.sup", ""); w.Code != 500 {
		t.Fatalf("video as sup: %d", w.Code)
	}
}
