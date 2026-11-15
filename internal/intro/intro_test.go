package intro

import (
	"math/rand"
	"testing"

	"lex/internal/store"
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

// Two episode tails (different lengths, different windows) that end with
// the same 75 s of credits, one of them followed by a 20 s preview.
func TestMatchTail(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	secs := func(x float64) int { return int(x / pointSecs) }
	noise := func(n int) []uint32 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = r.Uint32()
		}
		return out
	}
	credits := noise(secs(75))
	mk := func(pre, post int) []uint32 {
		out := noise(pre)
		for _, v := range credits {
			out = append(out, v^(1<<uint(r.Intn(32))))
		}
		out = append(out, noise(post)...)
		copy(out[pre:], credits[:10])
		return out
	}
	// Episode A: 1320 s long, last 420 s analysed; credits at 1245-1320.
	a := mk(secs(345), 0)
	offA, durA := 1320-float64(len(a))*pointSecs, 1320.0
	// Episode B: 1400 s long, last 300 s analysed; credits at 1305-1380, then a preview.
	b := mk(secs(205), secs(20))
	offB, durB := 1400-float64(len(b))*pointSecs, 1400.0

	sa, ea, sb, eb, ok := matchTail(a, b, offA, offB, durA, durB, 15)
	if !ok {
		t.Fatal("no match")
	}
	if sa < 1243 || sa > 1247 || ea != durA {
		t.Fatalf("bad credits in a: %.1f-%.1f", sa, ea)
	}
	if sb < 1303 || sb > 1307 || eb < 1377 || eb > 1382 {
		t.Fatalf("bad credits in b: %.1f-%.1f", sb, eb)
	}
	// Different endings must not match.
	if _, _, _, _, ok := matchTail(a, noise(len(b)), offA, offB, durA, durB, 15); ok {
		t.Fatal("matched noise")
	}
	// A shared stretch shorter than the minimum isn't credits.
	if _, _, _, _, ok := matchTail(a, b, offA, offB, durA, durB, 90); ok {
		t.Fatal("matched credits shorter than the minimum")
	}
}

func TestChapterCredits(t *testing.T) {
	file := func(dur float64, chaps ...store.Chapter) *store.File {
		return &store.File{Info: &store.MediaInfo{Duration: dur, Chapters: chaps}}
	}
	// Last chapter without an end runs to the end of the file.
	seg := chapterCredits(file(1400, store.Chapter{Start: 0, Title: "Prologue"}, store.Chapter{Start: 60, Title: "Part A"}, store.Chapter{Start: 1310, Title: "Ending"}))
	if seg == nil || seg.Kind != "credits" || seg.Start != 1310 || seg.End != 1400 || seg.Source != "chapter" {
		t.Fatalf("got %+v", seg)
	}
	// An "Ending" chapter in the middle isn't the credits.
	if seg := chapterCredits(file(1400, store.Chapter{Start: 300, End: 400, Title: "Ending"})); seg != nil {
		t.Fatalf("mid-file chapter taken for credits: %+v", seg)
	}
	// "Ending" is a credits chapter, never an intro.
	if seg := chapterIntro(file(1400, store.Chapter{Start: 1310, End: 1400, Title: "End Credits"})); seg != nil {
		t.Fatalf("credits taken for intro: %+v", seg)
	}
}

// pass: a chapter wins, a match fills both episodes, and episodes already
// analysed are neither changed nor handed a neighbour's result.
func TestPass(t *testing.T) {
	ep := func(n int, done bool, chaps ...store.Chapter) *episode {
		return &episode{item: &store.Item{Episode: n}, creditsDone: done,
			file: &store.File{Info: &store.MediaInfo{Duration: 1400, Chapters: chaps}}}
	}
	eps := []*episode{
		ep(1, false), ep(2, false), ep(3, true), ep(4, false, store.Chapter{Start: 1300, Title: "Credits"}), ep(5, false),
	}
	var compared [][2]int
	k := kindPass{
		done:    func(e *episode) bool { return e.creditsDone },
		result:  func(e *episode) **Segment { return &e.credits },
		chapter: chapterCredits,
		fp:      func(e *episode) []uint32 { return []uint32{uint32(e.item.Episode)} },
		match: func(a, b *episode) (*Segment, *Segment) {
			compared = append(compared, [2]int{a.item.Episode, b.item.Episode})
			// Episode 5 matches nothing.
			if a.item.Episode == 5 || b.item.Episode == 5 {
				return nil, nil
			}
			return &Segment{Kind: "credits", Start: float64(a.item.Episode)}, &Segment{Kind: "credits", Start: float64(b.item.Episode)}
		},
	}
	(&Detector{}).pass(t.Context(), eps, k)
	if eps[0].credits == nil || eps[1].credits == nil || eps[1].credits.Start != 2 {
		t.Fatalf("episodes 1-2 not matched: %+v %+v", eps[0].credits, eps[1].credits)
	}
	if eps[2].credits != nil {
		t.Fatalf("analysed episode changed: %+v", eps[2].credits)
	}
	if eps[3].credits == nil || eps[3].credits.Source != "chapter" {
		t.Fatalf("chapter not used: %+v", eps[3].credits)
	}
	if eps[4].credits != nil {
		t.Fatalf("episode 5 matched: %+v", eps[4].credits)
	}
	// Episode 2 got its result from episode 1, so only one comparison is
	// needed for the pair; episode 5 tries its neighbours.
	if len(compared) == 0 || compared[0] != [2]int{1, 2} {
		t.Fatalf("comparisons: %v", compared)
	}
	for _, c := range compared {
		if c[0] == 2 {
			t.Fatalf("episode 2 compared again: %v", compared)
		}
	}
}
