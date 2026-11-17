package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

// supSegment builds one PGS segment.
func supSegment(pts uint32, typ byte, payload []byte) []byte {
	b := []byte{'P', 'G', 0, 0, 0, 0, 0, 0, 0, 0, typ, 0, 0}
	binary.BigEndian.PutUint32(b[2:], pts)
	binary.BigEndian.PutUint16(b[11:], uint16(len(payload)))
	return append(b, payload...)
}

func be16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }

// testSup is a 1920x1080 track: a 4x2 white box at (100, 900) from 1s to
// 3s, then an empty display set clearing it.
func testSup() []byte {
	var b []byte
	pcs := func(pts uint32, state byte, objects int) []byte {
		p := append(append(be16(1920), be16(1080)...), 0x10)
		p = append(p, be16(1)...)
		p = append(p, state, 0, 0, byte(objects))
		if objects > 0 {
			p = append(p, be16(0)...)
			p = append(p, 0, 0)
			p = append(append(p, be16(100)...), be16(900)...)
		}
		return supSegment(pts, 0x16, p)
	}
	wds := supSegment(90000, 0x17, append(append(append(append([]byte{1, 0}, be16(100)...), be16(900)...), be16(4)...), be16(2)...))
	pds := supSegment(90000, 0x14, []byte{0, 0, 0, 16, 128, 128, 0, 1, 235, 128, 128, 255})
	rle := []byte{1, 1, 1, 1, 0, 0, 1, 1, 1, 1, 0, 0}
	ods := append(append([]byte{0, 0, 0, 0xC0, 0, 0, byte(len(rle) + 4)}, append(be16(4), be16(2)...)...), rle...)
	b = append(b, pcs(90000, 0x80, 1)...)
	b = append(b, wds...)
	b = append(b, pds...)
	b = append(b, supSegment(90000, 0x15, ods)...)
	b = append(b, supSegment(90000, 0x80, nil)...)
	b = append(b, pcs(270000, 0x00, 0)...)
	b = append(b, supSegment(270000, 0x80, nil)...)
	return b
}

// testdata/pgs.sup (used by the API tests) is testSup's output.
func TestPGSFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "pgs.sup"))
	if err != nil || !bytes.Equal(b, testSup()) {
		t.Fatalf("testdata/pgs.sup is stale (err %v); rewrite it from testSup", err)
	}
}

func TestCompleteDisplaySets(t *testing.T) {
	sup := testSup()
	if got := CompleteDisplaySets(sup); !bytes.Equal(got, sup) {
		t.Fatalf("whole file: %d of %d bytes", len(got), len(sup))
	}
	// Cut anywhere inside the second display set: only the first is kept.
	first := bytes.Index(sup[1:], []byte{'P', 'G', 0, 0x04, 0x1E, 0xB0}) + 1 // PCS at 3s
	for cut := first; cut < len(sup); cut++ {
		if got := CompleteDisplaySets(sup[:cut]); len(got) != first {
			t.Fatalf("cut at %d: kept %d, want %d", cut, len(got), first)
		}
	}
	if got := CompleteDisplaySets(sup[:first-1]); got == nil || len(got) != 0 {
		t.Fatalf("no complete set: %v", got)
	}
	if got := CompleteDisplaySets([]byte("garbage")); got == nil || len(got) != 0 {
		t.Fatalf("garbage: %v", got)
	}
}

func TestDecideDrawsPGS(t *testing.T) {
	cfg := store.DefaultConfig()
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	aac := store.Stream{Index: 1, Codec: "aac", Channels: 2, Default: true, CodecString: "mp4a.40.2"}
	f := file("/m/a.mp4", h264, aac)
	f.Info.Streams = append(f.Info.Streams,
		store.Stream{Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle"},
		store.Stream{Index: 3, Type: "subtitle", Codec: "dvd_subtitle"})
	caps := chrome
	caps.PGS = true

	p, err := Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: 2, Caps: caps}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != "direct" || p.BurnSubs || p.DrawSubs != "pgs" || !strings.Contains(strings.Join(p.Reasons, ";"), "drawn in the browser") {
		t.Fatalf("PGS drawn: %+v", p)
	}
	// VobSub, a player that can't draw, and native HLS still burn in.
	for name, req := range map[string]PlanRequest{
		"vobsub":  {Audio: -1, Subtitle: 3, Caps: caps},
		"no caps": {Audio: -1, Subtitle: 2, Caps: chrome},
		"hls":     {Audio: -1, Subtitle: 2, Caps: caps, ForceHLS: true},
	} {
		p, err := Decide(cfg, f, req, false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !p.BurnSubs || p.DrawSubs != "" || p.Method != "transcode" {
			t.Errorf("%s: method %s burn %v draw %q", name, p.Method, p.BurnSubs, p.DrawSubs)
		}
	}
}

// End to end: a PGS track muxed into MKV comes back out as the same .sup.
func TestServeSup(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	supPath := filepath.Join(dir, "in.sup")
	if err := os.WriteFile(supPath, testSup(), 0o600); err != nil {
		t.Fatal(err)
	}
	mkv := filepath.Join(dir, "a.mkv")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=black:s=320x180:d=5", "-copyts", "-i", supPath,
		"-map", "0", "-map", "1", "-c:v", "libx264", "-preset", "ultrafast", "-c:s", "copy", "-y", mkv).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg can't mux PGS here: %v %s", err, out)
	}
	s := NewSubs(filepath.Join(dir, "subs"), "ffmpeg", "ffprobe", logx.New(10, false))
	os.MkdirAll(filepath.Join(dir, "subs"), 0o700)
	f := &store.File{ID: 7, Path: mkv, Mtime: 1, Info: &store.MediaInfo{Streams: []store.Stream{
		{Index: 0, Type: "video", Codec: "h264"}, {Index: 1, Type: "subtitle", Codec: "hdmv_pgs_subtitle"}}}}
	p, data, partial, err := s.ServeSup(context.Background(), f, 1, 30*time.Second)
	if err != nil || partial || data != nil || p == "" {
		t.Fatalf("ServeSup = %q %v %v %v", p, data, partial, err)
	}
	got, _ := os.ReadFile(p)
	// Same segments; ffmpeg may rewrite the DTS fields.
	want := testSup()
	if len(got) != len(want) || CompleteDisplaySets(got) == nil {
		t.Fatalf("extracted %d bytes, want %d", len(got), len(want))
	}
	for off := 0; off < len(want); {
		n := 13 + int(binary.BigEndian.Uint16(want[off+11:]))
		if !bytes.Equal(got[off:off+2], want[off:off+2]) || !bytes.Equal(got[off+10:off+n], want[off+10:off+n]) || binary.BigEndian.Uint32(got[off+2:]) != binary.BigEndian.Uint32(want[off+2:]) {
			t.Fatalf("segment at %d differs:\n got % x\nwant % x", off, got[off:off+n], want[off:off+n])
		}
		off += n
	}
	// A text track isn't PGS.
	if _, _, _, err := s.ServeSup(context.Background(), f, 0, time.Second); err == nil {
		t.Fatal("served a video stream as PGS")
	}
}
