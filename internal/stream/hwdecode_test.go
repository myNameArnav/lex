package stream

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHEVCDecodeIssue(t *testing.T) {
	dir := t.TempDir()
	if hevcDecodeIssue(dir) == "" {
		t.Fatal("no /dev/media*: want an issue")
	}
	if err := os.WriteFile(filepath.Join(dir, "media1"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := hevcDecodeIssue(dir); got != "" {
		t.Fatalf("with media1: got issue %q", got)
	}
}

// Real FFmpeg 7.1 (Raspberry Pi build) output when /dev/media* is missing.
const fallbackStderr = `[hevc @ 0x5589cc4310] probe_media_device: opening /dev/media0 failed, No such file or directory (2)
[hevc @ 0x5589cc4310] probe_media_device: opening /dev/media1 failed, No such file or directory (2)
[hevc @ 0x5589cc4310] Failed setup for format drm_prime: hwaccel initialisation returned error.`

func TestHWDecodeFailure(t *testing.T) {
	for _, l := range []string{
		"[hevc @ 0x1] Failed setup for format drm_prime: hwaccel initialisation returned error.",
		"[hevc @ 0x1] Failed to find a device for V4L2 HEVC stateless V4",
	} {
		if !hwDecodeFailure(l) {
			t.Errorf("not detected: %q", l)
		}
	}
	for _, l := range []string{"", "frame=  10 fps=0.0", "[aac @ 0x1] Too many bits per frame"} {
		if hwDecodeFailure(l) {
			t.Errorf("false positive: %q", l)
		}
	}
}

func TestCheckHWFallback(t *testing.T) {
	var logged []string
	HWFallbackLog = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	t.Cleanup(func() {
		HWFallbackLog = nil
		hwFallback.Lock()
		hwFallback.last = nil
		hwFallback.Unlock()
	})

	// Software-only runs never count, whatever stderr says.
	CheckHWFallback("hls", []string{"-i", "x"}, fallbackStderr)
	if LastHWFallback() != nil {
		t.Fatal("recorded a fallback without -hwaccel")
	}
	CheckHWFallback("hls", []string{"-hwaccel", "drm", "-i", "x"}, "")
	if LastHWFallback() != nil {
		t.Fatal("recorded a fallback for clean stderr")
	}

	args := []string{"-hwaccel", "drm", "-i", "x"}
	CheckHWFallback("hls", args, fallbackStderr)
	CheckHWFallback("trickplay", args, fallbackStderr)
	f := LastHWFallback()
	if f == nil || f.Source != "trickplay" || f.Count != 2 || !strings.Contains(f.Message, "hwaccel initialisation returned error") {
		t.Fatalf("fallback = %+v", f)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "fell back to software (hls)") {
		t.Fatalf("want one warning, got %q", logged)
	}
}
