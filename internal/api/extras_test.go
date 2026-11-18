package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"lex/internal/cache"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/trickplay"
)

// A movie's local trailer is listed on its page, plays through the normal
// plan, and watching it saves nothing for the movie.
func TestLocalExtras(t *testing.T) {
	dir := t.TempDir()
	s, h := securityServer(t)
	s.DataDir = dir
	s.Sess = stream.NewManager(s.St, s.Log)
	s.Cache = cache.New(s.St, s.Log, t.TempDir())
	s.HLS = stream.NewHLS(filepath.Join(dir, "hls"), "ffmpeg", s.Log)
	s.Trick = trickplay.New(s.St, s.Log, "ffmpeg", filepath.Join(dir, "trickplay"))
	s.Subs = stream.NewSubs(filepath.Join(dir, "subs"), "ffmpeg", "ffprobe", s.Log)
	admin, _ := s.St.CreateInitialAdmin("admin", "test-password-123")
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{dir})
	movieDir := filepath.Join(dir, "Heat (1995)")
	os.MkdirAll(movieDir, 0o755)
	itemID, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Year: 1995, Path: movieDir})
	info := &store.MediaInfo{Format: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 600, Bitrate: 1_000_000, Streams: []store.Stream{
		{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, BitDepth: 8, CodecString: "avc1.640028", Default: true},
		{Index: 1, Type: "audio", Codec: "aac", Channels: 2, CodecString: "mp4a.40.2", Default: true},
	}}
	add := func(name, extra string) int64 {
		t.Helper()
		p := filepath.Join(movieDir, name)
		os.WriteFile(p, []byte("x"), 0o644)
		id, err := s.St.InsertFile(&store.File{ItemID: itemID, LibraryID: lib.ID, Path: p, Size: 1, Mtime: 1, Extra: extra})
		if err != nil {
			t.Fatal(err)
		}
		s.St.SaveProbe(id, info, "")
		return id
	}
	mainID := add("Heat (1995).mp4", "")
	trailerID := add("Heat (1995)-trailer.mp4", "trailer")
	add("Making.of.Heat.1080p.mp4", "featurette")

	w := adminRequest(t, h, token, "GET", "/api/items/"+strconv.FormatInt(itemID, 10), "")
	var detail struct {
		Files  []struct{ ID int64 }
		Extras []extraView
	}
	json.Unmarshal(w.Body.Bytes(), &detail)
	if len(detail.Files) != 1 || detail.Files[0].ID != mainID {
		t.Fatalf("files = %+v, want only the movie itself", detail.Files)
	}
	if len(detail.Extras) != 2 || detail.Extras[0] != (extraView{ID: trailerID, Type: "trailer", Title: "Trailer", Duration: 600}) ||
		detail.Extras[1].Title != "Making of Heat" {
		t.Fatalf("extras = %+v", detail.Extras)
	}

	plan := func(fileID int64) (int, string) {
		body, _ := json.Marshal(map[string]any{"itemId": itemID, "fileId": fileID, "audio": -1, "subtitle": -1, "sessionId": "s1",
			"caps": map[string]any{"mse": true, "direct": map[string]bool{"mp4": true}, "video": map[string]bool{"h264": true},
				"nativeVideo": map[string]bool{"h264": true}, "audio": map[string]bool{"aac": true}}})
		w := adminRequest(t, h, token, "POST", "/api/playback/plan", string(body))
		var res struct{ Subtitle string }
		json.Unmarshal(w.Body.Bytes(), &res)
		return w.Code, res.Subtitle
	}
	if code, sub := plan(trailerID); code != 200 || sub != "Trailer" {
		t.Fatalf("plan trailer: %d %q", code, sub)
	}
	if sess := s.Sess.Get("s1"); sess == nil || !sess.Extra || sess.FileID != trailerID {
		t.Fatalf("session = %+v, want the trailer as an extra", sess)
	}
	if _, err := s.Sess.Heartbeat("s1", admin.ID, 120, false, stream.ClientStats{}); err != nil {
		t.Fatal(err)
	}
	if ud, _ := s.St.GetUserData(admin.ID, itemID); ud.Position != 0 || ud.Played {
		t.Fatalf("watching the trailer saved %+v for the movie", ud)
	}
	// Playing the movie itself still picks its own file.
	if code, sub := plan(0); code != 200 || sub != "1995" {
		t.Fatalf("plan movie: %d %q", code, sub)
	}
	if sess := s.Sess.Get("s1"); sess == nil || sess.Extra || sess.FileID != mainID {
		t.Fatalf("session = %+v, want the movie", sess)
	}
	// Only extras have a frame-grab thumbnail.
	if w := adminRequest(t, h, token, "GET", "/api/files/"+strconv.FormatInt(mainID, 10)+"/thumb", ""); w.Code != 404 {
		t.Fatalf("thumb of the movie's file: %d", w.Code)
	}
}
