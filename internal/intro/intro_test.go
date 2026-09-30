package intro

import (
	"math/rand"
	"testing"
)

// Two synthetic fingerprints that share a 40 s stretch at different offsets.
func TestMatch(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	secs := func(x float64) int { return int(x / pointSecs) }
	n40, n60, n95 := secs(40), secs(60), secs(95)
	intro := make([]uint32, n40)
	for i := range intro {
		intro[i] = r.Uint32()
	}
	mk := func(pre, post int) []uint32 {
		var out []uint32
		for i := 0; i < pre; i++ {
			out = append(out, r.Uint32())
		}
		for _, v := range intro {
			// Flip a couple of bits: re-encoded audio never matches exactly.
			out = append(out, v^(1<<uint(r.Intn(32))))
		}
		for i := 0; i < post; i++ {
			out = append(out, r.Uint32())
		}
		return out
	}
	a := mk(n60, 2000)
	b := mk(n95, 2000)
	// Add exact anchors so candidate shifts exist (real audio has many).
	copy(a[n60:], intro[:10])
	copy(b[n95:], intro[:10])
	sa, ea, sb, eb, ok := match(a, b, 12, 130)
	if !ok {
		t.Fatal("no match")
	}
	if sa < 58 || sa > 62 || ea-sa < 38 || ea-sa > 41 || sb < 93 || sb > 97 || eb-sb < 38 {
		t.Fatalf("bad match a=%.1f-%.1f b=%.1f-%.1f", sa, ea, sb, eb)
	}
	// Unrelated audio must not match.
	c := make([]uint32, 5000)
	for i := range c {
		c[i] = r.Uint32()
	}
	if _, _, _, _, ok := match(a, c, 12, 130); ok {
		t.Fatal("matched noise")
	}
}
