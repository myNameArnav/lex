package stream

import (
	"strings"
	"testing"
)

func TestAssToVTT(t *testing.T) {
	in := "\ufeff[Script Info]\nPlayResY: 1080\n\n[V4+ Styles]\nFormat: Name, Fontsize\nStyle: Default,48\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,Hello {\\i1}there{\\i0}\\Nfriend\n" +
		"Dialogue: 0,0:00:02.50,0:00:03.00,Default,,0,0,0,,Hello {\\i1}there{\\i0}\\Nfriend\n" + // merged with the previous line
		"Dialogue: 0,0:00:01.00,0:00:05.00,Signs,,0,0,0,,{\\pos(10,10)}A SIGN\n" +
		"Dialogue: 0,0:00:01.00,0:00:05.00,ED04,,0,0,0,,{\\k20}la la\n" +
		"Dialogue: 0,0:00:01.00,0:00:05.00,Default,,0,0,0,,{\\p1}m 0 0 l 10 10{\\p0}\n" +
		"Dialogue: 0,0:00:04.00,0:00:05.00,Default,,0,0,0,,{\\pos(960,900)\\frz10}Rotated sign\n" +
		"Dialogue: 0,0:01:04.00,0:01:05.25,Default,,0,0,0,,a < b & c\n"
	got := string(AssToVTT([]byte(in)))
	want := "WEBVTT\n\n" +
		"00:00:01.000 --> 00:00:03.000\nHello <i>there</i>\nfriend\n\n" +
		"00:01:04.000 --> 00:01:05.250\na &lt; b &amp; c\n\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "SIGN") {
		t.Fatal("sign kept")
	}
}
