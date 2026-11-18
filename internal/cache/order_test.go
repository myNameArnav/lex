package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lex/internal/store"
)

// order runs next to the end, copying each range it picks, and returns the
// ranges' starts in MB.
func order(s *copyState, size int64, jumpAt map[int]int64) []int64 {
	var starts []int64
	for i := 0; ; i++ {
		if j, ok := jumpAt[i]; ok {
			s.jump = j
		}
		a, b, ok := s.next(size)
		if !ok {
			return starts
		}
		starts = append(starts, a>>20)
		s.add(a, b)
		s.cursor = b
	}
}

func TestCopyOrderFollowsPlayback(t *testing.T) {
	const mb = 1 << 20
	// Playback reads at 5 MB, right after the copy started at 0: the copy
	// moves there, goes to the end, then fills in the start.
	s := newCopyState()
	got := order(s, 8*mb, map[int]int64{1: 5 * mb})
	want := []int64{0, 5, 6, 7, 1, 2, 3, 4}
	if !slicesEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if s.total() != 8*mb || len(s.spans) != 1 {
		t.Fatalf("spans %v", s.spans)
	}
	// A seek forward from 3 to 7 mid-run: 7 to the end, then the gap after
	// where that run started (none), then from the start (4-6 left).
	s = newCopyState()
	got = order(s, 10*mb, map[int]int64{0: 2 * mb, 2: 7 * mb})
	want = []int64{2, 3, 7, 8, 9, 0, 1, 4, 5, 6}
	if !slicesEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func slicesEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReadingMovesTheCopy(t *testing.T) {
	c, _, _ := testCache(t, 0)
	job := &Job{FileID: 1, Name: "x.mkv", Size: 1 << 30, state: newCopyState()}
	c.current = job
	f := &store.File{ID: 1, Size: 1 << 30}
	s := job.state
	s.add(0, 10<<20)
	s.cursor = 10 << 20

	c.Reading(f, 900<<20, 64<<10) // the MKV index at the end: small
	c.Reading(f, 20<<20, -1)      // the copy is about to get there
	c.Reading(f, 5<<20, -1)       // already copied
	if s.jump != -1 {
		t.Fatalf("moved to %d", s.jump)
	}
	c.Reading(f, 500<<20+123, -1) // a seek forward past the copy
	if s.jump != 500<<20 {
		t.Fatalf("jump %d", s.jump)
	}
	s.next(job.Size)
	s.cursor = 501 << 20
	s.add(500<<20, 501<<20)
	c.Reading(f, 100<<20, -1) // a seek back: read from the original
	if s.jump != -1 {
		t.Fatalf("seek back moved the copy to %d", s.jump)
	}
	c.current = nil
}

// A whole copy made in jumping order is byte for byte the original.
func TestCopyWithJumpIsComplete(t *testing.T) {
	c, st, item := testCache(t, 0)
	src := filepath.Join(t.TempDir(), "Heat.mkv")
	data := make([]byte, 6<<20+1234)
	for i := range data {
		data[i] = byte(i * 7)
	}
	os.WriteFile(src, data, 0o644)
	cfg := st.Config()
	cfg.CacheSpeedMBs = 2
	st.SaveConfig(cfg)
	f := addFile(t, st, item, src, int64(len(data)))
	c.Request(f, "playing")
	deadline := time.Now().Add(10 * time.Second)
	for !c.Pending(f.ID) || c.Status().Current == nil || c.Status().Current.Done == 0 {
		if c.IsCached(f.ID) || time.Now().After(deadline) {
			t.Fatal("copy didn't start in time to move")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.Reading(f, 5<<20, -1)
	for !c.IsCached(f.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("not cached: %s", c.Status().LastError)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(c.Peek(f))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("copy differs (%d bytes, %v)", len(got), err)
	}
}
