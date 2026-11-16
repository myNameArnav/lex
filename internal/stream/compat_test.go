package stream

import (
	"reflect"
	"testing"

	"lex/internal/store"
)

func TestAssess(t *testing.T) {
	cfg := store.DefaultConfig()
	cfg.ClientAudio, cfg.ClientRemux = true, true
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	hevc10 := store.Stream{Index: 0, Codec: "hevc", BitDepth: 10, Width: 3840, Height: 2160, CodecString: "hvc1.2.4.L153.B0"}
	aac := store.Stream{Index: 1, Codec: "aac", Channels: 2, Default: true, CodecString: "mp4a.40.2"}
	dts := store.Stream{Index: 1, Codec: "dts", Channels: 6, Default: true}
	eac3 := store.Stream{Index: 1, Codec: "eac3", Channels: 6, Default: true, CodecString: "ec-3"}
	browser := chrome
	browser.ClientRemux = []string{"mkv"}
	browser.ClientAudio = []string{"eac3"}
	noHEVC := chrome
	noHEVC.Video = map[string]bool{"h264": true}
	noHEVC.NativeVideo = map[string]bool{"h264": true}

	cases := []struct {
		name   string
		f      *store.File
		caps   Caps
		req    PlanRequest
		tier   string
		causes []Cause
	}{
		{"plays as is", file("/m/a.mp4", h264, aac), chrome, PlanRequest{}, TierDirect, []Cause{}},
		{"server remuxes mkv", file("/m/a.mkv", h264, aac), chrome, PlanRequest{}, TierRemux, []Cause{{"direct", "container", "mkv"}}},
		{"server converts audio", file("/m/a.mkv", h264, dts), chrome, PlanRequest{}, TierRemux,
			[]Cause{{"direct", "container", "mkv"}, {"stream", "audio", "dts"}}},
		{"browser remuxes and converts audio", file("/m/a.mkv", h264, eac3), browser, PlanRequest{HLSCopy: true}, TierBrowser, []Cause{{"direct", "container", "mkv"}}},
		{"no hevc decoder", file("/m/a.mp4", hevc10, aac), noHEVC, PlanRequest{}, TierTranscode,
			[]Cause{{"direct", "video", "hevc10"}, {"stream", "video", "hevc10"}}},
		{"quality limit", file("/m/a.mp4", h264, aac), chrome, PlanRequest{MaxBitrate: 3000}, TierTranscode,
			[]Cause{{"direct", "bitrate", ""}, {"stream", "bitrate", ""}}},
		{"forced transcode", file("/m/a.mp4", h264, aac), chrome, PlanRequest{Mode: "transcode"}, TierTranscode,
			[]Cause{{"direct", "mode", "transcode"}, {"stream", "mode", "transcode"}}},
		{"prefer direct is ignored", file("/m/a.mkv", h264, aac), chrome, PlanRequest{Mode: "direct"}, TierRemux, []Cause{{"direct", "container", "mkv"}}},
		{"hls without keyframe index", file("/m/a.mp4", h264, aac), Caps{NativeVideo: map[string]bool{"h264": true}, NativeAudio: map[string]bool{"aac": true}}, PlanRequest{}, TierTranscode,
			[]Cause{{"direct", "container", "mp4"}, {"stream", "hls", "mp4"}}},
		{"not analysed", &store.File{Path: "/m/a.mkv"}, chrome, PlanRequest{}, TierUnknown, []Cause{}},
	}
	for _, c := range cases {
		c.req.Audio, c.req.Subtitle, c.req.Caps = -1, -1, c.caps
		got := Assess(cfg, c.f, c.req, false)
		if got.Tier != c.tier || !reflect.DeepEqual(got.Causes, c.causes) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, got.Tier, got.Causes, c.tier, c.causes)
		}
	}

	cfg.EnableTranscode = false
	want := []Cause{{"direct", "video", "hevc10"}, {"stream", "video", "hevc10"}, {"stream", "setting", "transcode"}}
	if got := Assess(cfg, file("/m/a.mp4", hevc10, aac), PlanRequest{Audio: -1, Subtitle: -1, Caps: noHEVC}, false); got.Tier != TierUnplayable || !reflect.DeepEqual(got.Causes, want) {
		t.Errorf("transcoding off: got %s %v, want %v", got.Tier, got.Causes, want)
	}
}
