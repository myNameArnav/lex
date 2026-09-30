package stream

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func TestFontFileName(t *testing.T) {
	for _, tc := range []struct{ tag, mime, want string }{
		{"Arial.ttf", "", "Arial.ttf"},
		{"../../etc/evil.ttf", "", "evil.ttf"},
		{"/abs/path/x.otf", "", "x.otf"},
		{`..\..\win.TTF`, "", "win.ttf"},
		{"..hidden.ttf", "", "hidden.ttf"},
		{"..", "application/x-truetype-font", "font.ttf"},
		{"", "application/vnd.ms-opentype", "font.otf"},
		{"noext", "font/sfnt", "noext.ttf"},
		{"Fancy Font (Bold)?#.ttf", "", "Fancy_Font__Bold___.ttf"},
		{"日本語.ttf", "", "___.ttf"},
		{"cover.jpg", "image/jpeg", ""},
		{"notes.txt", "", ""},
		{strings.Repeat("a", 300) + ".ttf", "", strings.Repeat("a", 64) + ".ttf"},
	} {
		if got := fontFileName(tc.tag, tc.mime); got != tc.want {
			t.Errorf("fontFileName(%q, %q) = %q, want %q", tc.tag, tc.mime, got, tc.want)
		}
	}
}

// A crafted filename tag must not let the font dump write outside the fonts
// folder.
func TestFontsStayInFolder(t *testing.T) {
	needFFmpeg(t)
	root := t.TempDir()
	font := filepath.Join(root, "font.bin")
	os.WriteFile(font, []byte("not really a font"), 0o644)
	mkv := filepath.Join(root, "media", "video.mkv")
	os.MkdirAll(filepath.Dir(mkv), 0o755)
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2", "-c:v", "ffv1"}
	for i, a := range [][2]string{
		{"../../../escape.ttf", "application/x-truetype-font"},
		{"dir/Same.ttf", "application/x-truetype-font"},
		{"same.TTF", "application/x-truetype-font"},
		{"cover.jpg", "image/jpeg"},
	} {
		args = append(args, "-attach", font, "-metadata:s:t:"+string(rune('0'+i)), "filename="+a[0], "-metadata:s:t:"+string(rune('0'+i)), "mimetype="+a[1])
	}
	args = append(args, "-y", mkv)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("make test file: %v %s", err, out)
	}
	s := NewSubs(filepath.Join(root, "cache", "subs"), "ffmpeg", "ffprobe", logx.New(10, false))
	dir, names, err := s.Fonts(t.Context(), &store.File{ID: 1, Path: mkv})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if want := []string{"Same.ttf", "escape.ttf", "same-2.ttf"}; !slices.Equal(names, want) {
		t.Fatalf("fonts = %v, want %v", names, want)
	}
	for _, n := range names {
		if b, _ := os.ReadFile(filepath.Join(dir, n)); string(b) != "not really a font" {
			t.Fatalf("%s = %q", n, b)
		}
	}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Name() == "escape.ttf" && filepath.Dir(p) != dir {
			t.Errorf("attachment written outside the fonts folder: %s", p)
		}
		return nil
	})
}

// fakeFFmpeg writes a script that fills every *.tmp output with a partial
// subtitle and exits with code.
func fakeFFmpeg(t *testing.T, code string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in *.tmp) printf 'WEBVTT\\n\\n00:00:01.000 --> 00:00:02.000\\nhalf' > \"$a\";; esac; done\nexit " + code + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFailedExtractionIsNotCached(t *testing.T) {
	f := &store.File{ID: 7, Mtime: 1, Path: "/nonexistent.mkv", Info: &store.MediaInfo{Streams: []store.Stream{
		{Index: 2, Type: "subtitle", Codec: "subrip", TextSub: true},
		{Index: 3, Type: "subtitle", Codec: "ass", TextSub: true},
	}}}
	s := NewSubs(t.TempDir(), fakeFFmpeg(t, "1"), "ffprobe", logx.New(10, false))
	if err := s.extractAll(f); err == nil {
		t.Fatal("failed extraction reported success")
	}
	ents, _ := os.ReadDir(s.dir)
	if len(ents) != 0 {
		t.Fatalf("failed extraction left %v", ents)
	}

	s.ffmpeg = fakeFFmpeg(t, "0")
	if err := s.extractAll(f); err != nil {
		t.Fatal(err)
	}
	if !exists(s.cachePath(f, 2)) || !exists(s.cachePathExt(f, 3, "ass")) {
		t.Fatal("successful extraction not cached")
	}
}

func TestAssVTTConcurrent(t *testing.T) {
	s := NewSubs(t.TempDir(), "ffmpeg", "ffprobe", logx.New(10, false))
	f := &store.File{ID: 1, Mtime: 1}
	src := s.cachePathExt(f, 3, "ass")
	ass := "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello\n"
	os.WriteFile(src, []byte(ass), 0o644)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, _, err := s.assVTT(f, 3, src); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b, _ := os.ReadFile(s.cachePath(f, 3))
	if string(b) != string(AssToVTT([]byte(ass))) {
		t.Fatalf("vtt = %q", b)
	}
	ents, _ := os.ReadDir(s.dir)
	if len(ents) != 2 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestConvertExternalSRT(t *testing.T) {
	needFFmpeg(t)
	s := NewSubs(t.TempDir(), "ffmpeg", "ffprobe", logx.New(10, false))
	src := filepath.Join(t.TempDir(), "sub.srt")
	os.WriteFile(src, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"), 0o644)
	f := &store.File{ID: 4, Mtime: 9}
	p, err := s.GetFile(t.Context(), f, 2005, src)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "WEBVTT") || !strings.Contains(string(b), "Hello") {
		t.Fatalf("vtt = %q", b)
	}
	s.Forget(4, 2005)
	if exists(p) {
		t.Fatal("Forget kept the cached subtitle")
	}
}

func TestForgetOnlyMatchingIndex(t *testing.T) {
	s := NewSubs(t.TempDir(), "ffmpeg", "ffprobe", logx.New(10, false))
	for _, n := range []string{"5-1-2001.vtt", "5-2-2001.vtt", "5-1-12001.vtt", "55-1-2001.vtt", "5-1-2001.ass", "5-1-2002.vtt"} {
		os.WriteFile(filepath.Join(s.dir, n), []byte("x"), 0o644)
	}
	s.Forget(5, 2001)
	var left []string
	ents, _ := os.ReadDir(s.dir)
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if want := []string{"5-1-12001.vtt", "5-1-2002.vtt", "55-1-2001.vtt"}; !slices.Equal(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}
