package stream

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The Raspberry Pi's stateless HEVC decoder (/dev/video19) is found through
// the media controller (/dev/mediaN), so FFmpeg needs those nodes as well.
// Without them `-hwaccel drm` quietly decodes on the CPU. This file detects
// that, both up front and from FFmpeg's stderr while it runs.

// hevcDecodeIssue explains why the HEVC decoder can't work even though
// /dev/video19 exists, or returns "". devDir is /dev outside tests.
func hevcDecodeIssue(devDir string) string {
	media, _ := filepath.Glob(filepath.Join(devDir, "media[0-9]*"))
	if len(media) == 0 {
		return "/dev/video19 is present but no /dev/media* device is, so FFmpeg can't find the HEVC decoder and decodes in software. " +
			"In a container, also pass /dev/media0 and /dev/media1 (and mount /run/udev read-only)."
	}
	return ""
}

// hwDecodeFailure reports whether an FFmpeg stderr line says hardware
// decoding couldn't start (FFmpeg then carries on in software).
func hwDecodeFailure(line string) bool {
	return strings.Contains(line, "hwaccel initialisation returned error") ||
		strings.Contains(line, "probe_video_device: opening") ||
		strings.Contains(line, "Failed to find a device for") ||
		(strings.Contains(line, "Failed setup for format") && strings.Contains(line, "hwaccel"))
}

// HWDecodeAborted reports whether a failed FFmpeg run that used -hwaccel
// failed because of the hardware decoder, so the same job may work in
// software. Besides the usual setup errors, FFmpeg gives up outright when
// a media controller lists a video node it can't open (bcm2835-codec's
// image_fx at /dev/video18, which the container doesn't get).
func HWDecodeAborted(args []string, stderr string) bool {
	if !usesHWAccel(args) {
		return false
	}
	for line := range strings.SplitSeq(stderr, "\n") {
		if hwDecodeFailure(line) {
			return true
		}
	}
	return false
}

func usesHWAccel(args []string) bool { return slices.Contains(args, "-hwaccel") }

// HWFallback is the last time a hardware decode fell back to software.
type HWFallback struct {
	Source  string    `json:"source"` // transcode, hls, trickplay
	Message string    `json:"message"`
	At      time.Time `json:"at"`
	Count   int       `json:"count"`
}

var hwFallback struct {
	sync.Mutex
	last *HWFallback
}

// HWFallbackLog, when set, receives a warning the first time a hardware
// decode falls back to software (and again after an hour of quiet).
var HWFallbackLog func(format string, args ...any)

// NoteHWFallback records a software fallback seen in FFmpeg output.
func NoteHWFallback(source, message string) {
	hwFallback.Lock()
	prev := hwFallback.last
	f := &HWFallback{Source: source, Message: strings.TrimSpace(message), At: time.Now(), Count: 1}
	if prev != nil {
		f.Count = prev.Count + 1
	}
	hwFallback.last = f
	hwFallback.Unlock()
	if HWFallbackLog != nil && (prev == nil || f.At.Sub(prev.At) > time.Hour) {
		HWFallbackLog("hardware decode fell back to software (%s): %s; check that /dev/media* and /run/udev are passed to the container", source, f.Message)
	}
}

// LastHWFallback returns the most recent software fallback, or nil.
func LastHWFallback() *HWFallback {
	hwFallback.Lock()
	defer hwFallback.Unlock()
	if hwFallback.last == nil {
		return nil
	}
	f := *hwFallback.last
	return &f
}

// CheckHWFallback scans a finished FFmpeg run's stderr and records a
// fallback if it used -hwaccel and hardware decoding didn't start.
func CheckHWFallback(source string, args []string, stderr string) {
	if !usesHWAccel(args) {
		return
	}
	for line := range strings.SplitSeq(stderr, "\n") {
		if hwDecodeFailure(line) {
			NoteHWFallback(source, line)
			return
		}
	}
}
