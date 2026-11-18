package cache

import (
	"cmp"
	"slices"

	"lex/internal/store"
)

// Copy order. A copy for playback starts where the viewer is, not at the
// start of the file: the copy and the playback then read the same part of
// the hard drive (no seeking between two places), and the copy soon runs
// ahead of the play position, so playback moves to the SSD (OpenPlayback)
// within seconds. It copies from there to the end, then fills in the start.
// A seek forward past the copy moves it there; a seek back reads from the
// original until the fill-in gets there.

const (
	// jumpMin: reads shorter than this (container headers, the MKV index
	// at the end of a file) don't move the copy.
	jumpMin = 4 << 20
	// jumpNear: a read this close ahead of the copy waits for it instead.
	jumpNear = 32 << 20
	chunk    = 1 << 20
)

type span struct{ start, end int64 }

// copyState is where a copy has got to; guarded by Cache.mu.
type copyState struct {
	spans  []span // copied, sorted, merged
	cursor int64  // next offset to copy
	anchor int64  // where the current run started (the play position)
	jump   int64  // requested new cursor, or -1
	moves  int    // jumps made, for the log
}

func newCopyState() *copyState { return &copyState{jump: -1} }

// covered is how many bytes from off are copied (0 if off isn't).
func (s *copyState) covered(off int64) int64 {
	for _, sp := range s.spans {
		if off >= sp.start && off < sp.end {
			return sp.end - off
		}
	}
	return 0
}

func (s *copyState) total() int64 {
	var n int64
	for _, sp := range s.spans {
		n += sp.end - sp.start
	}
	return n
}

func (s *copyState) add(start, end int64) {
	s.spans = append(s.spans, span{start, end})
	slices.SortFunc(s.spans, func(a, b span) int { return cmp.Compare(a.start, b.start) })
	out := s.spans[:1]
	for _, sp := range s.spans[1:] {
		if last := &out[len(out)-1]; sp.start <= last.end {
			last.end = max(last.end, sp.end)
		} else {
			out = append(out, sp)
		}
	}
	s.spans = out
}

// gap is the first offset at or after from that isn't copied, or size.
func (s *copyState) gap(from, size int64) int64 {
	for _, sp := range s.spans {
		if from >= sp.start && from < sp.end {
			from = sp.end
		}
	}
	return min(from, size)
}

// next picks the range to copy next: from the cursor (or a requested jump)
// up to the next copied span. Past the end it goes back to the first gap
// after where this run started, then to the first gap from the start.
// ok is false when the whole file is copied.
func (s *copyState) next(size int64) (start, end int64, ok bool) {
	if s.jump >= 0 {
		s.cursor, s.anchor, s.jump = s.jump, s.jump, -1
	}
	pos := s.gap(s.cursor, size)
	if pos >= size {
		pos = s.gap(s.anchor, size)
	}
	if pos >= size {
		s.anchor = 0
		pos = s.gap(0, size)
	}
	if pos >= size {
		return 0, 0, false
	}
	end = min(pos+chunk, size)
	for _, sp := range s.spans {
		if sp.start > pos {
			end = min(end, sp.start)
			break
		}
	}
	s.cursor = pos
	return pos, end, true
}

// Reading tells the cache that playback is reading f from off (length -1:
// to the end). If f is being copied and that part isn't copied or about to
// be, the copy moves there. Reads behind the copy don't move it: they come
// from the original until the copy fills them in.
func (c *Cache) Reading(f *store.File, off, length int64) {
	if length >= 0 && length < jumpMin {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.current
	if job == nil || job.FileID != f.ID || job.Size != f.Size || job.state == nil {
		return
	}
	s := job.state
	if s.covered(off) > 0 || off < s.cursor || off-s.cursor < jumpNear || off >= job.Size {
		return
	}
	s.jump = off / chunk * chunk
	s.moves++
	c.log.Infof("cache: copy of %s moves to %d MB, where playback reads", job.Name, off>>20)
}
