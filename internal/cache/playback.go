package cache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lex/internal/store"
)

// copyPaths are where a file's cached copy goes, and its name while copying.
func (c *Cache) copyPaths(f *store.File) (dst, tmp string) {
	dst = filepath.Join(c.Dir(), fmt.Sprintf("%d%s", f.ID, strings.ToLower(filepath.Ext(f.Path))))
	return dst, dst + ".part"
}

// Source is a media file opened for playback (OpenPlayback).
type Source interface {
	io.ReadSeekCloser
	ModTime() time.Time
}

// OpenPlayback opens a file for playback: its complete cached copy if
// there is one, else the original. While the copy is still being made, the
// part already copied is read from it, so playback leaves the slow disk as
// soon as the copy passes the play position instead of when the whole file
// is done (two readers on one hard drive slow each other down).
func (c *Cache) OpenPlayback(f *store.File) (Source, error) {
	p := c.Resolve(f)
	orig, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	st, err := orig.Stat()
	if err != nil {
		orig.Close()
		return nil, err
	}
	src := &splitReader{orig: orig, size: st.Size(), mtime: st.ModTime()}
	if p != f.Path {
		return src, nil // the complete copy
	}
	if part, avail, still, ok := c.partial(f); ok {
		if pf, err := os.Open(part); err == nil {
			// The copy may have ended, and a new one for the same file
			// made a fresh part file, between finding it and opening it:
			// then avail describes another file, so don't use it.
			if still() {
				src.part, src.avail = pf, avail
			} else {
				pf.Close()
			}
		}
	}
	return src, nil
}

// partial is the copy of f being made now, if any: its part file and how
// many bytes from an offset are copied in it. That stays answerable after
// the copy ends (the part file stays readable through an open handle).
func (c *Cache) partial(f *store.File) (path string, avail func(off int64) int64, current func() bool, ok bool) {
	if !c.enabled() {
		return "", nil, nil, false
	}
	c.mu.Lock()
	job := c.current
	c.mu.Unlock()
	if job == nil || job.FileID != f.ID || job.Size != f.Size {
		return "", nil, nil, false
	}
	current = func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.current == job
	}
	_, tmp := c.copyPaths(f)
	return tmp, func(off int64) int64 {
		c.mu.Lock()
		defer c.mu.Unlock()
		if job.state == nil {
			return 0
		}
		return job.state.covered(off)
	}, current, true
}

// splitReader reads a file from its part-made copy where that is copied,
// and from the original elsewhere.
type splitReader struct {
	orig, part *os.File
	avail      func(off int64) int64
	size, off  int64
	mtime      time.Time
}

func (r *splitReader) ModTime() time.Time { return r.mtime }

func (r *splitReader) Read(b []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if rest := r.size - r.off; int64(len(b)) > rest {
		b = b[:rest]
	}
	if r.part != nil {
		if a := r.avail(r.off); a > 0 {
			if int64(len(b)) > a {
				b = b[:a]
			}
			if n, _ := r.part.ReadAt(b, r.off); n > 0 {
				r.off += int64(n)
				return n, nil
			}
			// The copy was removed or cut short: read the original.
			r.part.Close()
			r.part = nil
		}
	}
	n, err := r.orig.ReadAt(b, r.off)
	r.off += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}

func (r *splitReader) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += r.off
	case io.SeekEnd:
		off += r.size
	}
	if off < 0 {
		return 0, errors.New("seek before start")
	}
	r.off = off
	return off, nil
}

func (r *splitReader) Close() error {
	if r.part != nil {
		r.part.Close()
	}
	return r.orig.Close()
}
