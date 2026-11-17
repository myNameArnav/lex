package stream

import (
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"lex/internal/store"
)

func TestVoiceDownmix(t *testing.T) {
	got := VoiceDownmix(&store.Stream{ChannelLayout: "5.1(side)"})
	want := "pan=stereo|FL<0.6*FL+1*FC+0.4*SL|FR<0.6*FR+1*FC+0.4*SR"
	if got != want {
		t.Fatalf("5.1(side):\n got %s\nwant %s", got, want)
	}
	for _, l := range []string{"", "stereo", "mono", "quad", "weird"} {
		if f := VoiceDownmix(&store.Stream{ChannelLayout: l}); f != "" {
			t.Errorf("%q: %s, want none", l, f)
		}
	}
}

// Every filter VoiceDownmix makes must run in ffmpeg on its layout.
func TestVoiceDownmixRunsInFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	for l := range layoutChannels {
		f := VoiceDownmix(&store.Stream{ChannelLayout: l})
		out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "anullsrc=sample_rate=48000:channel_layout="+l,
			"-t", "0.1", "-af", f, "-ac", "2", "-f", "null", "-").CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "" {
			t.Errorf("%s (%s): %v %s", l, f, err, out)
		}
	}
}

func TestDecideVoiceBoost(t *testing.T) {
	cfg := store.DefaultConfig()
	h264 := store.Stream{Index: 0, Codec: "h264", BitDepth: 8, Width: 1920, Height: 1080, CodecString: "avc1.640028"}
	surround := store.Stream{Index: 1, Codec: "eac3", Channels: 6, ChannelLayout: "5.1(side)", Default: true, CodecString: "ec-3"}
	stereo := store.Stream{Index: 1, Codec: "eac3", Channels: 2, ChannelLayout: "stereo", Default: true, CodecString: "ec-3"}
	req := PlanRequest{Audio: -1, Subtitle: -1, Caps: chrome, VoiceBoost: true}
	plan, err := Decide(cfg, file("/m/a.mkv", h264, surround), req, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.URL, "vb=1") || !strings.Contains(plan.AudioOut, "voices boosted") {
		t.Fatalf("plan url %s audioOut %q", plan.URL, plan.AudioOut)
	}
	req.VoiceBoost = false
	if plan, _ := Decide(cfg, file("/m/a.mkv", h264, surround), req, false); strings.Contains(plan.URL, "vb=1") {
		t.Fatalf("boost off: %s", plan.URL)
	}
	// Stereo has no centre channel to lift.
	req.VoiceBoost = true
	if plan, _ := Decide(cfg, file("/m/a.mkv", h264, stereo), req, false); strings.Contains(plan.URL, "vb=1") {
		t.Fatalf("stereo: %s", plan.URL)
	}
	// 5.1 kept as 5.1 (Settings → audio channels 6) isn't downmixed.
	cfg.AudioChannels = 6
	if plan, _ := Decide(cfg, file("/m/a.mkv", h264, surround), req, false); strings.Contains(plan.URL, "vb=1") {
		t.Fatalf("5.1 out: %s", plan.URL)
	}
	if p := ParseParams(1, mustQuery(t, "vb=1")); !p.Voice {
		t.Fatal("vb=1 not parsed")
	}
}

func mustQuery(t *testing.T, s string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(s)
	if err != nil {
		t.Fatal(err)
	}
	return q
}
