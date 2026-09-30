package stream

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"lex/internal/library"
	"lex/internal/logx"
	"lex/internal/proc"
	"lex/internal/store"
)

// Subs extracts subtitles to WebVTT and caches them on disk.
type Subs struct {
	dir    string
	ffmpeg string
	log    *logx.Logger
	mu     sync.Mutex
	flight map[string]*extraction
	// Resolve maps a file to the path to read (e.g. an SSD cache copy).
	Resolve func(*store.File) string
}

type extraction struct {
	done chan struct{}
	err  error
}

func NewSubs(dir, ffmpeg string, log *logx.Logger) *Subs {
	os.MkdirAll(dir, 0o755)
	return &Subs{dir: dir, ffmpeg: ffmpeg, log: log, flight: map[string]*extraction{}}
}

func (s *Subs) cachePath(f *store.File, idx int) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d-%d-%d.vtt", f.ID, f.Mtime, idx))
}

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// Get returns a path to a WebVTT file for the given subtitle stream.
func (s *Subs) Get(ctx context.Context, f *store.File, idx int) (string, error) {
	dst := s.cachePath(f, idx)
	if exists(dst) {
		return dst, nil
	}
	if idx >= 1000 {
		for _, ext := range library.ExternalSubs(f.Path) {
			if ext.Index == idx {
				return dst, s.run(ctx, fmt.Sprintf("ext-%d-%d", f.ID, idx), func() error { return s.convertExternal(ext.ExternalPath, dst) })
			}
		}
		return "", fmt.Errorf("subtitle %d not found", idx)
	}
	if f.Info == nil {
		return "", fmt.Errorf("file not probed")
	}
	st := f.Info.StreamByIndex(idx)
	if st == nil || st.Type != "subtitle" {
		return "", fmt.Errorf("subtitle %d not found", idx)
	}
	if !st.TextSub {
		return "", fmt.Errorf("%s subtitles are images and can only be burned in", st.Codec)
	}
	err := s.run(ctx, fmt.Sprintf("emb-%d", f.ID), func() error { return s.extractAll(f) })
	if err != nil {
		return "", err
	}
	if !exists(dst) {
		return "", fmt.Errorf("subtitle extraction produced no output")
	}
	return dst, nil
}

// Prefetch starts extracting embedded text subtitles in the background so
// they're ready when the viewer turns them on.
func (s *Subs) Prefetch(f *store.File) {
	if f.Info == nil {
		return
	}
	need := false
	for _, st := range f.Info.Streams {
		if st.Type == "subtitle" && st.TextSub && !exists(s.cachePath(f, st.Index)) {
			need = true
		}
	}
	if !need {
		return
	}
	go s.run(context.Background(), fmt.Sprintf("emb-%d", f.ID), func() error { return s.extractAll(f) })
}

// run de-duplicates concurrent work under key and waits for it (or ctx).
func (s *Subs) run(ctx context.Context, key string, fn func() error) error {
	s.mu.Lock()
	e, ok := s.flight[key]
	if !ok {
		e = &extraction{done: make(chan struct{})}
		s.flight[key] = e
		go func() {
			e.err = fn()
			s.mu.Lock()
			delete(s.flight, key)
			s.mu.Unlock()
			close(e.done)
		}()
	}
	s.mu.Unlock()
	select {
	case <-e.done:
		return e.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Subs) extractAll(f *store.File) error {
	src := f.Path
	if s.Resolve != nil {
		src = s.Resolve(f)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", src}
	type out struct{ tmp, dst string }
	var outs []out
	for _, st := range f.Info.Streams {
		if st.Type != "subtitle" || !st.TextSub {
			continue
		}
		dst := s.cachePath(f, st.Index)
		if exists(dst) {
			continue
		}
		tmp := dst + ".tmp"
		outs = append(outs, out{tmp, dst})
		args = append(args, "-map", fmt.Sprintf("0:%d", st.Index), "-c:s", "webvtt", "-f", "webvtt", "-y", tmp)
	}
	if len(outs) == 0 {
		return nil
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	proc.Nice(cmd.Process.Pid, 5)
	err := cmd.Wait()
	for _, o := range outs {
		if exists(o.tmp) {
			os.Rename(o.tmp, o.dst)
		} else {
			os.Remove(o.tmp)
		}
	}
	if err != nil {
		return fmt.Errorf("subtitle extraction failed: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	s.log.Infof("extracted %d subtitle track(s) from %s in %s", len(outs), filepath.Base(f.Path), time.Since(start).Round(time.Millisecond))
	return nil
}

func (s *Subs) convertExternal(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(src), ".vtt") && utf8.Valid(b) {
		return os.WriteFile(dst, b, 0o644)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if !utf8.Valid(b) {
		args = append(args, "-sub_charenc", "CP1252")
	}
	args = append(args, "-i", src, "-c:s", "webvtt", "-f", "webvtt", "-y", dst+".tmp")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(dst + ".tmp")
		return fmt.Errorf("convert subtitle: %v %s", err, strings.TrimSpace(string(out)))
	}
	return os.Rename(dst+".tmp", dst)
}

// Clear removes all cached subtitles.
func (s *Subs) Clear() error {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		os.Remove(filepath.Join(s.dir, e.Name()))
	}
	return nil
}
