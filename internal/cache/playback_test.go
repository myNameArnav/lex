package cache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A reader over a copy written up to some point reads that part from the
// copy and the rest from the original, across seeks.
func TestSplitReaderUsesCopiedPart(t *testing.T) {
	dir := t.TempDir()
	want := bytes.Repeat([]byte("0123456789"), 1000)
	origPath, partPath := filepath.Join(dir, "orig"), filepath.Join(dir, "part")
	os.WriteFile(origPath, want, 0o644)
	// The copy's bytes are marked, to see which file each byte came from.
	marked := bytes.ToUpper(bytes.Repeat([]byte("abcdefghij"), 400))
	os.WriteFile(partPath, marked, 0o644)
	orig, _ := os.Open(origPath)
	part, _ := os.Open(partPath)
	var written atomic.Int64
	written.Store(4000)
	r := &splitReader{orig: orig, part: part, written: written.Load, size: int64(len(want))}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || !bytes.Equal(got[:4000], marked) || !bytes.Equal(got[4000:], want[4000:]) {
		t.Fatal("expected the first 4000 bytes from the copy and the rest from the original")
	}
	// The copy grows: a later read of that range comes from it.
	written.Store(int64(len(marked)))
	r.Seek(3990, io.SeekStart)
	b := make([]byte, 20)
	n, _ := io.ReadFull(r, b)
	if n != 20 || !bytes.Equal(b[:10], marked[3990:4000]) {
		t.Fatalf("read across the copy's end: %q", b[:n])
	}
	if end, _ := r.Seek(0, io.SeekEnd); end != int64(len(want)) {
		t.Fatalf("size %d", end)
	}
}

// A copy that disappears mid-read falls back to the original.
func TestSplitReaderWithoutCopy(t *testing.T) {
	dir := t.TempDir()
	want := []byte("hello world")
	origPath := filepath.Join(dir, "orig")
	os.WriteFile(origPath, want, 0o644)
	orig, _ := os.Open(origPath)
	part, _ := os.Open(origPath)
	part.Close() // reads fail
	r := &splitReader{orig: orig, part: part, written: func() int64 { return 5 }, size: int64(len(want))}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// OpenPlayback reads the part copied so far while the copy runs.
func TestOpenPlaybackDuringCopy(t *testing.T) {
	c, st, item := testCache(t, 0)
	cfg := st.Config()
	cfg.CacheSpeedMBs = 1 // 1 MB/s: the 3 MB copy takes ~3s
	st.SaveConfig(cfg)
	src := filepath.Join(t.TempDir(), "Heat.mkv")
	data := bytes.Repeat([]byte{7}, 3<<20)
	os.WriteFile(src, data, 0o644)
	f := addFile(t, st, item, src, int64(len(data)))
	c.Request(f, "playing")
	deadline := time.Now().Add(5 * time.Second)
	var written func() int64
	for written == nil || written() < 1<<20 {
		if time.Now().After(deadline) {
			t.Fatal("copy didn't start")
		}
		time.Sleep(20 * time.Millisecond)
		if _, w, ok := c.partial(f); ok {
			written = w
		}
	}
	s, err := c.OpenPlayback(f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sr := s.(*splitReader)
	if sr.part == nil {
		t.Fatal("not reading from the copy in progress")
	}
	got, err := io.ReadAll(s)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}
	c.Remove(f.ID)
}

// A copy of another file pauses while playback waits on the disk, and the
// copy of the file being played does not.
func TestCopyYieldsToPlayback(t *testing.T) {
	c, _, _ := testCache(t, 0)
	var busy atomic.Bool
	busy.Store(true)
	c.Contended = func(fileID int64) bool { return busy.Load() && fileID != 1 }
	job := &Job{FileID: 2, Name: "next.mkv", Size: 10}
	done := make(chan error, 1)
	go func() { done <- c.yield(t.Context(), job) }()
	select {
	case <-done:
		t.Fatal("didn't wait for playback")
	case <-time.After(1500 * time.Millisecond):
	}
	busy.Store(false)
	select {
	case err := <-done:
		if err != nil || job.paused < time.Second {
			t.Fatalf("err %v, paused %v", err, job.paused)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("still waiting after playback recovered")
	}
	busy.Store(true)
	if err := c.yield(t.Context(), &Job{FileID: 1}); err != nil {
		t.Fatal(err)
	}
}
