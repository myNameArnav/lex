package stream

import (
	"strings"
	"testing"

	"lex/internal/store"
)

func file(path string, v store.Stream, a ...store.Stream) *store.File {
	v.Type = "video"
	streams := []store.Stream{v}
	for _, s := range a {
		s.Type = "audio"
		streams = append(streams, s)
	}
	return &store.File{ID: 1, Path: path, Info: &store.MediaInfo{Duration: 100, Bitrate: 8_000_000, Streams: streams}}
}

var chrome = Caps{
	MSE:         true,
	Direct:      map[string]bool{"mp4": true, "webm": true},
	Video:       map[string]bool{"h264": true, "hevc": true, "hevc10": true},
	NativeVideo: map[string]bool{"h264": true, "hevc": true, "hevc10": true},
	Audio:       map[string]bool{"aac": true, "opus": true},
	NativeAudio: map[string]bool{"aac": true, "opus": true},
}

func TestDecide(t *testing.T) {
	cfg := store.DefaultConfig()
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	aac := store.Stream{Index: 1, Codec: "aac", Channels: 2, Default: true, CodecString: "mp4a.40.2"}
	eac3 := store.Stream{Index: 1, Codec: "eac3", Channels: 6, Default: true, CodecString: "ec-3"}
	mpeg4 := store.Stream{Index: 0, Codec: "mpeg4", BitDepth: 8, Width: 720, Height: 480}

	cases := []struct {
		name    string
		f       *store.File
		req     PlanRequest
		method  string
		vcopy   bool
		acopy   bool
		reason  string
		mimeHas string
	}{
		{"mp4 h264 aac direct", file("/m/a.mp4", h264, aac), PlanRequest{Audio: -1, Subtitle: -1}, "direct", true, true, "", ""},
		{"mkv needs remux", file("/m/a.mkv", h264, aac), PlanRequest{Audio: -1, Subtitle: -1}, "remux", true, true, "container mkv", "avc1.640028,mp4a.40.2"},
		{"eac3 audio transcoded", file("/m/a.mkv", h264, eac3), PlanRequest{Audio: -1, Subtitle: -1}, "remux", true, false, "audio codec eac3", "mp4a.40.2"},
		{"bitrate cap forces transcode", file("/m/a.mp4", h264, aac), PlanRequest{Audio: -1, Subtitle: -1, MaxBitrate: 3000}, "transcode", false, true, "over 3000 kbps", "avc1.64001F"},
		{"unsupported video transcodes", file("/m/a.avi", mpeg4, aac), PlanRequest{Audio: -1, Subtitle: -1}, "transcode", false, true, "video codec mpeg4", "avc1"},
		{"forced remux", file("/m/a.mp4", h264, aac), PlanRequest{Audio: -1, Subtitle: -1, Mode: "remux"}, "remux", true, true, "forced remux", ""},
	}
	for _, c := range cases {
		c.req.Caps = chrome
		p, err := Decide(cfg, c.f, c.req, false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.Method != c.method || p.VideoCopy != c.vcopy || p.AudioCopy != c.acopy {
			t.Errorf("%s: got %s vcopy=%v acopy=%v (%v)", c.name, p.Method, p.VideoCopy, p.AudioCopy, p.Reasons)
		}
		if c.reason != "" && !strings.Contains(strings.Join(p.Reasons, ";"), c.reason) {
			t.Errorf("%s: reasons %v missing %q", c.name, p.Reasons, c.reason)
		}
		if c.mimeHas != "" && !strings.Contains(p.Mime, c.mimeHas) {
			t.Errorf("%s: mime %q missing %q", c.name, p.Mime, c.mimeHas)
		}
	}
}

// Firefox can't decode E-AC-3, DTS or TrueHD but the player can convert
// them: the server copies them and asks for AAC from the browser.
func TestDecideClientAudio(t *testing.T) {
	cfg := store.DefaultConfig()
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	caps := chrome
	caps.ClientAudio = []string{"ac3", "eac3", "dts", "truehd"}
	for _, a := range []store.Stream{
		{Index: 1, Codec: "eac3", Channels: 6, Default: true, CodecString: "ec-3"},
		{Index: 1, Codec: "dts", Profile: "DTS-HD MA", Channels: 8, Default: true},
		{Index: 1, Codec: "truehd", Channels: 8, Default: true},
	} {
		t.Run(a.Codec, func(t *testing.T) {
			f := file("/m/a.mkv", h264, a)
			p, err := Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps}, false)
			if err != nil {
				t.Fatal(err)
			}
			want := ClientAudio{Codec: a.Codec, Channels: 2, Bitrate: 192}
			if p.Method != "remux" || !p.AudioCopy || p.ClientAudio == nil || *p.ClientAudio != want {
				t.Fatalf("got %s acopy=%v client=%+v", p.Method, p.AudioCopy, p.ClientAudio)
			}
			if p.Mime != `video/mp4; codecs="avc1.640028,mp4a.40.2"` {
				t.Errorf("mime %q should name the AAC the browser produces", p.Mime)
			}
			if !strings.Contains(strings.Join(p.Reasons, ";"), "audio codec "+a.Codec+" converted in the browser") {
				t.Errorf("reasons %v", p.Reasons)
			}

			// 5.1 output: 7.1 sources are mixed down to the configured count.
			six := cfg
			six.AudioChannels = 6
			p, _ = Decide(six, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps}, false)
			if p.ClientAudio == nil || p.ClientAudio.Channels != 6 {
				t.Errorf("5.1: client=%+v", p.ClientAudio)
			}

			// The Pi's hardware encoder goes through MPEG-TS, which carries
			// all of these: still copied.
			hw := cfg
			hw.VideoEncoder = "h264_v4l2m2m"
			p, _ = Decide(hw, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps, Mode: "transcode"}, false)
			if p.Method != "transcode" || p.ClientAudio == nil {
				t.Errorf("hardware transcode: %s client=%+v", p.Method, p.ClientAudio)
			}

			// Not reported by the browser, turned off, or HLS (no MSE to
			// feed): the server converts it.
			old := chrome
			old.ClientAudio = []string{"ac3"}
			p, _ = Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: old}, false)
			if p.AudioCopy || p.ClientAudio != nil {
				t.Errorf("not reported: acopy=%v client=%+v", p.AudioCopy, p.ClientAudio)
			}
			off := cfg
			off.ClientAudio = false
			p, _ = Decide(off, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps}, false)
			if p.AudioCopy || p.ClientAudio != nil {
				t.Errorf("disabled: acopy=%v client=%+v", p.AudioCopy, p.ClientAudio)
			}
			p, _ = Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps, ForceHLS: true}, false)
			if p.AudioCopy || p.ClientAudio != nil {
				t.Errorf("HLS: acopy=%v client=%+v", p.AudioCopy, p.ClientAudio)
			}
		})
	}
}

func TestRemoteLimit(t *testing.T) {
	cfg := store.DefaultConfig()
	cfg.RemoteMaxBitrate = 4000
	f := file("/m/a.mp4", store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"},
		store.Stream{Index: 1, Codec: "aac", Channels: 2, CodecString: "mp4a.40.2"})
	p, _ := Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: chrome}, true)
	if p.Method != "transcode" || p.Height != 720 {
		t.Fatalf("remote client should be capped to a 720p transcode, got %s %dp", p.Method, p.Height)
	}
	p, _ = Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: chrome}, false)
	if p.Method != "direct" {
		t.Fatalf("local client should direct play, got %s", p.Method)
	}
}
