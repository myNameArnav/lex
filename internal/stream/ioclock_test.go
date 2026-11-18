package stream

import (
	"io"
	"strings"
	"testing"
	"time"
)

// slowReader takes d per read, like a disk that can't keep up.
type slowReader struct {
	io.ReadSeeker
	d time.Duration
}

func (r slowReader) Read(b []byte) (int, error) {
	time.Sleep(r.d)
	return r.ReadSeeker.Read(b)
}

func TestDiskWaitTellsSlowDiskFromSlowNetwork(t *testing.T) {
	run := func(diskDelay, netDelay time.Duration) float64 {
		s := &Session{io: &sessionIO{}}
		s.sampleIO(time.Now())
		src := s.DiskReader(slowReader{strings.NewReader(strings.Repeat("x", 4096)), diskDelay})
		dst := s.NetWriter(writerFunc(func(b []byte) (int, error) { time.Sleep(netDelay); return len(b), nil }))
		buf := make([]byte, 1024)
		if _, err := io.CopyBuffer(struct{ io.Writer }{dst}, struct{ io.Reader }{src}, buf); err != nil {
			t.Fatal(err)
		}
		s.sampleIO(time.Now())
		return s.DiskWait
	}
	if w := run(40*time.Millisecond, time.Millisecond); w < 0.45 {
		t.Errorf("slow disk: DiskWait %.2f, want near 0.5 after one sample", w)
	}
	if w := run(time.Millisecond, 40*time.Millisecond); w > 0.1 {
		t.Errorf("slow network: DiskWait %.2f, want near 0", w)
	}
}

func TestIOClockCountsReadInProgress(t *testing.T) {
	var c ioClock
	start := c.begin()
	now := start + int64(3*time.Second)
	if got := c.busy(now); got != int64(3*time.Second) {
		t.Fatalf("busy %v, want 3s for a read still in progress", time.Duration(got))
	}
	c.end(start)
	if c.n.Load() != 0 || c.starts.Load() != 0 {
		t.Fatal("finished read left in progress")
	}
}

func TestSampleIOIgnoresIdle(t *testing.T) {
	s := &Session{io: &sessionIO{}, DiskWait: 0.8}
	s.sampleIO(time.Now())
	if s.DiskWait != 0.8 {
		t.Fatalf("DiskWait changed to %.2f with no I/O", s.DiskWait)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
