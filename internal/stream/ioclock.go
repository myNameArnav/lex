package stream

import (
	"io"
	"sync/atomic"
	"time"
)

// ioClock adds up how long reads (or writes) take, counting those still in
// progress, so a read stuck on a slow disk shows up before it returns.
// Overlapping calls (several open responses) each count in full.
type ioClock struct {
	done   atomic.Int64 // ns spent in finished calls
	starts atomic.Int64 // sum of the start times (unix ns) of calls in progress
	n      atomic.Int64 // calls in progress
}

func (c *ioClock) begin() int64 {
	t := time.Now().UnixNano()
	c.starts.Add(t)
	c.n.Add(1)
	return t
}

func (c *ioClock) end(t int64) {
	c.done.Add(time.Now().UnixNano() - t)
	c.starts.Add(-t)
	c.n.Add(-1)
}

// busy is the total time spent so far, as of now (unix ns).
func (c *ioClock) busy(now int64) int64 {
	return c.done.Load() + c.n.Load()*now - c.starts.Load()
}

type diskReader struct {
	io.ReadSeeker
	c *ioClock
}

func (r *diskReader) Read(b []byte) (int, error) {
	t := r.c.begin()
	defer r.c.end(t)
	return r.ReadSeeker.Read(b)
}

type netWriter struct {
	io.Writer
	c *ioClock
}

func (w *netWriter) Write(b []byte) (int, error) {
	t := w.c.begin()
	defer w.c.end(t)
	return w.Writer.Write(b)
}

// sessionIO is a session's disk and network clocks, behind a pointer so
// copies of the session (Get, Snapshot) share them.
type sessionIO struct{ disk, net ioClock }

// DiskReader times reads of the media file and NetWriter times writes to
// the player, for DiskWait: whether the server is waiting on its disk or on
// the network.
// Without clocks (a session not made by Open) they pass calls through.
func (s *Session) DiskReader(rs io.ReadSeeker) io.ReadSeeker {
	if s.io == nil {
		return rs
	}
	return &diskReader{rs, &s.io.disk}
}

func (s *Session) NetWriter(w io.Writer) io.Writer {
	if s.io == nil {
		return w
	}
	return &netWriter{w, &s.io.net}
}

// sampleIO updates DiskWait from the time spent reading the disk versus
// writing to the network since the last sample. Called with m.mu held.
func (s *Session) sampleIO(now time.Time) {
	if s.io == nil {
		return
	}
	ns := now.UnixNano()
	d, n := s.io.disk.busy(ns), s.io.net.busy(ns)
	dd, dn := max(d-s.lastDisk, 0), max(n-s.lastNet, 0)
	s.lastDisk, s.lastNet = d, n
	// Too little I/O to tell (idle, or the player's buffer is full).
	if dd+dn < int64(100*time.Millisecond) {
		return
	}
	share := float64(dd) / float64(dd+dn)
	s.DiskWait = s.DiskWait*0.5 + share*0.5
}
