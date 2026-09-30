package stream

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

// hlsTestFile makes a 40 s MKV (H.264 + AAC) whose keyframes are every
// 2.5 s, so copied segments can't be 6 s long.
func hlsTestFile(t *testing.T) *store.File {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	path := filepath.Join(t.TempDir(), "clip.mkv")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24:d=40",
		"-f", "lavfi", "-i", "sine=f=440:d=40", "-c:v", "libx264", "-preset", "ultrafast", "-g", "60", "-keyint_min", "60",
		"-sc_threshold", "0", "-c:a", "aac", "-shortest", path).CombinedOutput()
	if err != nil {
		t.Skipf("can't make test clip: %v %s", err, out)
	}
	st, _ := os.Stat(path)
	return &store.File{ID: 1, Path: path, Mtime: st.ModTime().Unix(), Size: st.Size(), Info: &store.MediaInfo{
		Duration: 40, Streams: []store.Stream{
			{Index: 0, Type: "video", Codec: "h264", Width: 320, Height: 180, CodecString: "avc1.64000d"},
			{Index: 1, Type: "audio", Codec: "aac", Channels: 1, CodecString: "mp4a.40.2"},
		}}}
}

// videoSpan returns the first and last video packet times in a segment.
func videoSpan(t *testing.T, path string) (first, last float64) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	lines := strings.Fields(strings.TrimSpace(string(out)))
	first, _ = strconv.ParseFloat(strings.TrimSuffix(lines[0], ","), 64)
	for _, l := range lines {
		v, _ := strconv.ParseFloat(strings.TrimSuffix(l, ","), 64)
		first, last = min(first, v), max(last, v)
	}
	return first, last
}

func newTestHLS(t *testing.T) *HLS {
	h := NewHLS(filepath.Join(t.TempDir(), "hls"), "ffmpeg", logx.New(50, false))
	t.Cleanup(func() {
		h.mu.Lock()
		for _, j := range h.jobs {
			h.dropLocked(j)
		}
		h.mu.Unlock()
	})
	return h
}

func TestHLSSegmentsMatchPlaylist(t *testing.T) {
	f := hlsTestFile(t)
	cfg := store.DefaultConfig()
	cfg.VideoEncoder = "libx264"
	ff := FFInfo{Encoders: []string{"libx264"}}
	for _, copy := range []bool{true, false} {
		h := newTestHLS(t)
		p := Params{FileID: 1, SessionID: "s-" + strconv.FormatBool(copy), VideoCopy: copy, AudioCopy: true, Audio: 1, Burn: -1, Bitrate: 800, Height: 180, Channels: 2}
		if copy && !h.CanCopy(f) {
			t.Fatal("no keyframe index for an MKV")
		}
		b := h.bounds(f, p)
		if copy {
			// Keyframes every 2.5 s: boundaries at 0, 7.5, 15, ...
			if len(b) < 3 || b[1] != 7.5 {
				t.Fatalf("copy boundaries = %v", b)
			}
		} else if b[1] != 6 || b[2] != 12 {
			t.Fatalf("transcode boundaries = %v", b)
		}
		pl := h.Playlist(f, p, nil)
		if !strings.Contains(pl, "#EXTINF:") || !strings.HasSuffix(pl, "#EXT-X-ENDLIST\n") {
			t.Fatalf("bad playlist:\n%s", pl)
		}
		// Segments 0-2 in order, then a seek to 4.
		for _, n := range []int{0, 1, 2, 4} {
			path, err := h.Segment(context.Background(), cfg, ff, f, p, n, 0)
			if err != nil {
				t.Fatalf("copy=%v segment %d: %v", copy, n, err)
			}
			first, last := videoSpan(t, path)
			frame := 1.0 / 24
			if d := first - b[n]; d < -0.001 || d > frame+0.001 {
				t.Errorf("copy=%v segment %d starts at %.3f, playlist says %.3f", copy, n, first, b[n])
			}
			if last >= b[n+1]-0.001 || last < b[n+1]-3*frame {
				t.Errorf("copy=%v segment %d ends at %.3f, next starts at %.3f", copy, n, last, b[n+1])
			}
		}
	}
}

func TestHLSRestartsForDeletedSegment(t *testing.T) {
	f := hlsTestFile(t)
	cfg := store.DefaultConfig()
	h := newTestHLS(t)
	p := Params{FileID: 1, SessionID: "rewind", VideoCopy: true, AudioCopy: true, Audio: 1, Burn: -1}
	ctx := context.Background()
	for n := 0; n <= 3; n++ {
		if _, err := h.Segment(ctx, cfg, FFInfo{}, f, p, n, 0); err != nil {
			t.Fatal(err)
		}
	}
	// The janitor deletes segments well behind the player; seeking back to
	// one used to wait 90 s for a file that was never rewritten.
	j := h.job(f, p)
	os.Remove(j.raw(1))
	os.Remove(j.served(1))
	done := make(chan error, 1)
	go func() { _, err := h.Segment(ctx, cfg, FFInfo{}, f, p, 1, 0); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("rewinding to a deleted segment hung")
	}
}

func TestHLSTranscodeLimitIsShared(t *testing.T) {
	f := hlsTestFile(t)
	cfg := store.DefaultConfig()
	cfg.VideoEncoder = "libx264"
	cfg.MaxTranscodes = 1
	h := newTestHLS(t)
	// An MSE transcode already holds the only slot.
	if !h.Slots.Acquire("mse:other:1", 1) {
		t.Fatal("first slot refused")
	}
	p := Params{FileID: 1, SessionID: "second", AudioCopy: true, Audio: 1, Burn: -1, Bitrate: 800, Height: 180, Channels: 2}
	if _, err := h.Segment(context.Background(), cfg, FFInfo{Encoders: []string{"libx264"}}, f, p, 0, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("second transcode: err = %v, want ErrLimit", err)
	}
	h.Slots.Release("mse:other:1")
	if _, err := h.Segment(context.Background(), cfg, FFInfo{Encoders: []string{"libx264"}}, f, p, 0, 0); err != nil {
		t.Fatalf("after the slot was freed: %v", err)
	}
	h.Stop("second")
	deadline := time.Now().Add(10 * time.Second)
	for h.Slots.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := h.Slots.Count(); n != 0 {
		t.Fatalf("slot still held after Stop: %d", n)
	}
}

func TestHLSConcurrentRequestsAndStop(t *testing.T) {
	f := hlsTestFile(t)
	cfg := store.DefaultConfig()
	h := newTestHLS(t)
	p := Params{FileID: 1, SessionID: "busy", VideoCopy: true, AudioCopy: true, Audio: 1, Burn: -1}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			h.Segment(ctx, cfg, FFInfo{}, f, p, n%4, 0)
		}(i)
	}
	time.Sleep(200 * time.Millisecond)
	h.Stop("busy")
	wg.Wait()
	// A stopped job can't be restarted by a late request.
	if _, err := h.Segment(context.Background(), cfg, FFInfo{}, f, p, 0, 0); err != nil {
		t.Fatalf("new request after Stop: %v", err)
	}
	if !validSID.MatchString("abc-123") || validSID.MatchString("") || validSID.MatchString("a|b") {
		t.Fatal("session id validation")
	}
}
