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
	if part, written, ok := c.partial(f); ok {
		if pf, err := os.Open(part); err == nil {
			src.part, src.written = pf, written
		}
	}
	return src, nil
}

// partial is the copy of f being made now, if any: its part file and how
// much of it is written so far. written keeps returning the last count once
// the copy ends (the part file stays readable through an open handle).
func (c *Cache) partial(f *store.File) (path string, written func() int64, ok bool) {
	if !c.enabled() {
		return "", nil, false
	}
	c.mu.Lock()
	job := c.current
	c.mu.Unlock()
	if job == nil || job.FileID != f.ID || job.Size != f.Size {
		return "", nil, false
	}
	_, tmp := c.copyPaths(f)
	var last int64
	return tmp, func() int64 {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.current == job {
			last = job.Done
		}
		return last
	}, true
}

// splitReader reads a file from its part-made copy where that is written,
// and from the original beyond it.
type splitReader struct {
	orig, part *os.File
	written    func() int64
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
		if w := r.written(); r.off < w {
			if int64(len(b)) > w-r.off {
				b = b[:w-r.off]
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
