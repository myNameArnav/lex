package stream

import (
	"fmt"
	"strings"

	"lex/internal/store"
)

// Voice boost for audio the server converts to stereo. A standard downmix
// mixes the centre channel (where films put dialogue) in at -3 dB beside
// everything else; this one weights it about 7 dB above the front channels
// and turns the surrounds down, so speech stays clear at a low volume.

// layoutChannels are ffmpeg's standard layouts that have a centre channel.
var layoutChannels = map[string]string{
	"3.0":            "FL+FR+FC",
	"4.0":            "FL+FR+FC+BC",
	"5.0":            "FL+FR+FC+BL+BR",
	"5.0(side)":      "FL+FR+FC+SL+SR",
	"5.1":            "FL+FR+FC+LFE+BL+BR",
	"5.1(side)":      "FL+FR+FC+LFE+SL+SR",
	"6.0":            "FL+FR+FC+BC+SL+SR",
	"6.1":            "FL+FR+FC+LFE+BC+SL+SR",
	"6.1(back)":      "FL+FR+FC+LFE+BL+BR+BC",
	"7.0":            "FL+FR+FC+BL+BR+SL+SR",
	"7.1":            "FL+FR+FC+LFE+BL+BR+SL+SR",
	"7.1(wide)":      "FL+FR+FC+LFE+BL+BR+FLC+FRC",
	"7.1(wide-side)": "FL+FR+FC+LFE+FLC+FRC+SL+SR",
	"5.1.2":          "FL+FR+FC+LFE+SL+SR+TFL+TFR",
	"5.1.2(back)":    "FL+FR+FC+LFE+BL+BR+TFL+TFR",
	"5.1.4":          "FL+FR+FC+LFE+SL+SR+TFL+TFR+TBL+TBR",
	"7.1.2":          "FL+FR+FC+LFE+BL+BR+SL+SR+TFL+TFR",
	"7.1.4":          "FL+FR+FC+LFE+BL+BR+SL+SR+TFL+TFR+TBL+TBR",
}

// Each channel's weight in the left (L) or right (R) output, or both (C).
// The LFE channel is left out, as in a standard downmix.
var voiceWeights = map[string]struct {
	side string
	w    float64
}{
	"FC": {"C", 1}, "BC": {"C", 0.3},
	"FL": {"L", 0.6}, "FLC": {"L", 0.6}, "BL": {"L", 0.4}, "SL": {"L", 0.4}, "TFL": {"L", 0.3}, "TBL": {"L", 0.3},
	"FR": {"R", 0.6}, "FRC": {"R", 0.6}, "BR": {"R", 0.4}, "SR": {"R", 0.4}, "TFR": {"R", 0.3}, "TBR": {"R", 0.3},
}

// VoiceDownmix returns the pan filter that downmixes the stream to stereo
// with the centre channel lifted, or "" when the stream has no centre
// channel or a layout it doesn't know.
func VoiceDownmix(a *store.Stream) string {
	if a == nil {
		return ""
	}
	chans, ok := layoutChannels[strings.TrimSpace(a.ChannelLayout)]
	if !ok {
		return ""
	}
	var l, r []string
	for _, c := range strings.Split(chans, "+") {
		vw, ok := voiceWeights[c]
		if !ok {
			continue
		}
		term := fmt.Sprintf("%g*%s", vw.w, c)
		if vw.side != "R" {
			l = append(l, term)
		}
		if vw.side != "L" {
			r = append(r, term)
		}
	}
	// "<" scales each output's weights to sum to 1, so it can't clip.
	return fmt.Sprintf("pan=stereo|FL<%s|FR<%s", strings.Join(l, "+"), strings.Join(r, "+"))
}
