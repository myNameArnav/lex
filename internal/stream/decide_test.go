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

// Firefox can't decode E-AC-3 but the player can convert it: the server
// copies it and asks for AAC from the browser.
func TestDecideClientAudio(t *testing.T) {
	cfg := store.DefaultConfig()
	f := file("/m/a.mkv", store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"},
		store.Stream{Index: 1, Codec: "eac3", Channels: 6, Default: true, CodecString: "ec-3"})
	caps := chrome
	caps.ClientAudio = []string{"ac3", "eac3"}
	p, err := Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := ClientAudio{Codec: "eac3", Channels: 2, Bitrate: 192}
	if p.Method != "remux" || !p.AudioCopy || p.ClientAudio == nil || *p.ClientAudio != want {
		t.Fatalf("got %s acopy=%v client=%+v", p.Method, p.AudioCopy, p.ClientAudio)
	}
	if p.Mime != `video/mp4; codecs="avc1.640028,mp4a.40.2"` {
		t.Errorf("mime %q should name the AAC the browser produces", p.Mime)
	}
	if !strings.Contains(strings.Join(p.Reasons, ";"), "converted in the browser") {
		t.Errorf("reasons %v", p.Reasons)
	}

	// Turned off, or HLS (no MSE to feed): the server converts it.
	off := cfg
	off.ClientAudio = false
	p, _ = Decide(off, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps}, false)
	if p.AudioCopy || p.ClientAudio != nil {
		t.Errorf("disabled: acopy=%v client=%+v", p.AudioCopy, p.ClientAudio)
	}
	p, _ = Decide(cfg, f, PlanRequest{Audio: -1, Subtitle: -1, Caps: caps, ForceHLS: true}, false)
	if p.ClientAudio != nil {
		t.Errorf("HLS: client=%+v", p.ClientAudio)
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

// Direct Stream without ffmpeg: for an MKV whose codecs the browser plays
// (or converts), the player reads the file and remuxes it itself.
func TestDecideClientRemux(t *testing.T) {
	cfg := store.DefaultConfig()
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	aac := store.Stream{Index: 2, Codec: "aac", Channels: 2, Default: true, CodecString: "mp4a.40.2"}
	eac3 := store.Stream{Index: 1, Codec: "eac3", Channels: 6, Default: true, CodecString: "ec-3"}
	caps := chrome
	caps.ClientAudio = []string{"ac3", "eac3"}
	caps.ClientRemux = []string{"mkv", "webm"}
	req := func(mod func(*PlanRequest)) PlanRequest {
		r := PlanRequest{Audio: -1, Subtitle: -1, Caps: caps, SessionID: "s1", HLSCopy: true}
		if mod != nil {
			mod(&r)
		}
		return r
	}

	p, err := Decide(cfg, file("/m/a.mkv", h264, aac), req(nil), false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != "remux" || !p.ClientRemux || !p.VideoCopy || !p.AudioCopy || p.Audio != 2 {
		t.Fatalf("got %s client=%v vcopy=%v acopy=%v audio=%d", p.Method, p.ClientRemux, p.VideoCopy, p.AudioCopy, p.Audio)
	}
	if p.URL != "/api/files/1/direct?sid=s1&via=remux" || p.FragmentMs != cfg.FragmentMs {
		t.Errorf("url %q fragment %d", p.URL, p.FragmentMs)
	}
	if p.Mime != `video/mp4; codecs="avc1.640028,mp4a.40.2"` {
		t.Errorf("mime %q should be the server remux's", p.Mime)
	}
	if !strings.Contains(strings.Join(p.Reasons, ";"), "remuxed in the browser") {
		t.Errorf("reasons %v", p.Reasons)
	}

	// Audio the player converts: remuxed and converted in the browser.
	p, _ = Decide(cfg, file("/m/a.mkv", h264, eac3), req(nil), false)
	if !p.ClientRemux || p.ClientAudio == nil || p.Mime != `video/mp4; codecs="avc1.640028,mp4a.40.2"` {
		t.Errorf("E-AC-3: client=%v audio=%+v mime %q", p.ClientRemux, p.ClientAudio, p.Mime)
	}
	// A forced Direct Stream is fine too; a file without audio as well.
	if p, _ = Decide(cfg, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.Mode = "remux" }), false); !p.ClientRemux {
		t.Errorf("forced remux: client=%v", p.ClientRemux)
	}
	if p, _ = Decide(cfg, file("/m/a.mkv", h264), req(nil), false); !p.ClientRemux || p.Mime != `video/mp4; codecs="avc1.640028"` {
		t.Errorf("no audio: client=%v mime %q", p.ClientRemux, p.Mime)
	}
	// What the browser plays as it is still direct plays.
	if p, _ = Decide(cfg, file("/m/a.mp4", h264, aac), req(nil), false); p.Method != "direct" || p.ClientRemux {
		t.Errorf("MP4: got %s client=%v", p.Method, p.ClientRemux)
	}

	dts := store.Stream{Index: 1, Codec: "dts", Channels: 6, Default: true, CodecString: "dtsc"}
	alac := store.Stream{Index: 1, Codec: "alac", Channels: 2, Default: true, CodecString: "alac"}
	alacCaps := caps
	alacCaps.Audio = map[string]bool{"aac": true, "alac": true}
	for _, c := range []struct {
		name   string
		cfg    func(*store.Config)
		f      *store.File
		req    PlanRequest
		method string
	}{
		{"setting off", func(c *store.Config) { c.ClientRemux = false }, file("/m/a.mkv", h264, aac), req(nil), "remux"},
		{"remux disabled", func(c *store.Config) { c.EnableRemux = false }, file("/m/a.mkv", h264, aac), req(nil), "transcode"},
		{"browser can't", nil, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.Caps.ClientRemux = nil }), "remux"},
		{"not matroska", nil, file("/m/a.avi", h264, aac), req(nil), "remux"},
		{"no keyframe index", nil, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.HLSCopy = false }), "remux"},
		{"HLS", nil, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.ForceHLS = true }), "remux"},
		{"forced transcode", nil, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.Mode = "transcode" }), "transcode"},
		{"bitrate limit", nil, file("/m/a.mkv", h264, aac), req(func(r *PlanRequest) { r.MaxBitrate = 3000 }), "transcode"},
		{"video unsupported", nil, file("/m/a.mkv", store.Stream{Index: 0, Codec: "mpeg2video", Width: 720, Height: 576}, aac), req(nil), "transcode"},
		{"burned-in subtitles", nil, &store.File{ID: 1, Path: "/m/a.mkv", Info: &store.MediaInfo{Duration: 100, Bitrate: 8_000_000,
			Streams: []store.Stream{{Index: 0, Type: "video", Codec: "h264", Width: 1920, Height: 1080, CodecString: "avc1.640028"}, {Index: 1, Type: "audio", Codec: "aac", CodecString: "mp4a.40.2"}, {Index: 2, Type: "subtitle", Codec: "hdmv_pgs_subtitle"}}}},
			req(func(r *PlanRequest) { r.Subtitle = 2 }), "transcode"},
		{"audio the server converts", nil, file("/m/a.mkv", h264, dts), req(nil), "remux"},
		{"audio the remuxer doesn't copy", nil, file("/m/a.mkv", h264, alac), req(func(r *PlanRequest) { r.Caps = alacCaps }), "remux"},
	} {
		cfg := store.DefaultConfig()
		if c.cfg != nil {
			c.cfg(&cfg)
		}
		p, err := Decide(cfg, c.f, c.req, false)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.Method != c.method || p.ClientRemux || p.FragmentMs != 0 || strings.Contains(p.URL, "/direct") {
			t.Errorf("%s: got %s client=%v url %q (%v)", c.name, p.Method, p.ClientRemux, p.URL, p.Reasons)
		}
	}
}
