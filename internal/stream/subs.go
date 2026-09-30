package stream

import (
	"bytes"
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
	return s.cachePathExt(f, idx, "vtt")
}

func (s *Subs) cachePathExt(f *store.File, idx int, ext string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%d-%d-%d.%s", f.ID, f.Mtime, idx, ext))
}

// IsASS reports whether a subtitle codec is SubStation Alpha (styled).
func IsASS(codec string) bool { return codec == "ass" || codec == "ssa" }

// GetASS returns a path to the raw ASS/SSA script for a styled subtitle
// stream, so the browser can render it with libass (JASSUB).
func (s *Subs) GetASS(ctx context.Context, f *store.File, idx int) (string, error) {
	if idx >= 1000 {
		for _, ext := range library.ExternalSubs(f.Path) {
			if ext.Index == idx && IsASS(ext.Codec) {
				return ext.ExternalPath, nil
			}
		}
		return "", fmt.Errorf("subtitle %d is not an ASS file", idx)
	}
	p, _, err := s.Serve(ctx, f, idx, "ass", 24*time.Hour)
	return p, err
}

// GetFile converts an arbitrary subtitle file (e.g. a downloaded one) to
// WebVTT, cached under the given index.
func (s *Subs) GetFile(ctx context.Context, f *store.File, idx int, src string) (string, error) {
	dst := s.cachePath(f, idx)
	if exists(dst) {
		return dst, nil
	}
	return dst, s.run(ctx, fmt.Sprintf("dl-%d-%d", f.ID, idx), func() error { return s.convertExternal(src, dst) })
}

var fontExts = map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".woff": true, ".woff2": true}

// FontsDir extracts the fonts attached to a file (Matroska attachments,
// common in anime releases) and returns the directory and file names.
func (s *Subs) Fonts(ctx context.Context, f *store.File) (string, []string, error) {
	dir := filepath.Join(s.dir, "fonts", fmt.Sprintf("%d-%d", f.ID, f.Mtime))
	list := func() []string {
		var out []string
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && fontExts[strings.ToLower(filepath.Ext(e.Name()))] {
				out = append(out, e.Name())
			}
		}
		return out
	}
	if _, err := os.Stat(filepath.Join(dir, ".done")); err == nil {
		return dir, list(), nil
	}
	err := s.run(ctx, fmt.Sprintf("fonts-%d", f.ID), func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		src := f.Path
		if s.Resolve != nil {
			src = s.Resolve(f)
		}
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// -dump_attachment writes each attachment under its filename tag into
		// the working directory; -t 0 stops right after reading the header.
		cmd := exec.CommandContext(cctx, s.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-dump_attachment:t", "", "-t", "0", "-i", src, "-f", "null", "-")
		cmd.Dir = dir
		cmd.Run() // ffmpeg exits non-zero when there's no output stream; the dump still happened
		// Drop anything that isn't a font (and any odd names).
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() || !fontExts[strings.ToLower(filepath.Ext(e.Name()))] {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
		return os.WriteFile(filepath.Join(dir, ".done"), nil, 0o644)
	})
	if err != nil {
		return "", nil, err
	}
	return dir, list(), nil
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
	p, _, err := s.Serve(ctx, f, idx, "vtt", 24*time.Hour)
	return p, err
}

// Prefetch starts extracting embedded text subtitles in the background so
// they're ready when the viewer turns them on.
func (s *Subs) Prefetch(f *store.File) {
	if f.Info == nil {
		return
	}
	need := false
	for _, st := range f.Info.Streams {
		if st.Type == "subtitle" && st.TextSub && !exists(s.embeddedPath(f, &st)) {
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
	e := s.start(key, fn)
	select {
	case <-e.done:
		return e.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// start begins fn under key unless it's already running.
func (s *Subs) start(key string, fn func() error) *extraction {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return e
}

// embeddedPath is where extraction writes an embedded text track: the raw
// script for ASS/SSA, WebVTT for everything else.
func (s *Subs) embeddedPath(f *store.File, st *store.Stream) string {
	if IsASS(st.Codec) {
		return s.cachePathExt(f, st.Index, "ass")
	}
	return s.cachePath(f, st.Index)
}

// Serve returns an embedded text subtitle track as "vtt" or "ass" (ASS
// tracks only). Extraction has to read the whole file, which takes a while
// for big files on a hard drive. If it's still running after wait, Serve
// returns what's been extracted so far as data (partial=true), and the
// client asks again later. Otherwise it returns the path of the finished file.
func (s *Subs) Serve(ctx context.Context, f *store.File, idx int, format string, wait time.Duration) (path string, data []byte, err error) {
	if f.Info == nil {
		return "", nil, fmt.Errorf("file not probed")
	}
	st := f.Info.StreamByIndex(idx)
	if st == nil || st.Type != "subtitle" {
		return "", nil, fmt.Errorf("subtitle %d not found", idx)
	}
	if !st.TextSub {
		return "", nil, fmt.Errorf("%s subtitles are images and can only be burned in", st.Codec)
	}
	ass := IsASS(st.Codec)
	if format == "ass" && !ass {
		return "", nil, fmt.Errorf("subtitle %d is not ASS", idx)
	}
	src := s.embeddedPath(f, st)
	finish := func() (string, []byte, error) {
		if !exists(src) {
			return "", nil, fmt.Errorf("subtitle extraction produced no output")
		}
		if format == "vtt" && ass {
			return s.assVTT(f, idx, src)
		}
		return src, nil, nil
	}
	if format == "vtt" && exists(s.cachePath(f, idx)) {
		return s.cachePath(f, idx), nil, nil
	}
	if exists(src) {
		return finish()
	}
	e := s.start(fmt.Sprintf("emb-%d", f.ID), func() error { return s.extractAll(f) })
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-e.done:
		if e.err != nil {
			return "", nil, e.err
		}
		return finish()
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case <-t.C:
	}
	// Still extracting: hand out the complete part of what's there.
	for {
		b, _ := os.ReadFile(src + ".tmp")
		switch {
		case !ass:
			if b = cutAt(b, "\n\n"); len(b) == 0 {
				b = []byte("WEBVTT\n\n")
			}
			return "", b, nil
		case bytes.Contains(b, []byte("[Events]")):
			b = cutAt(b, "\n")
			if format == "vtt" {
				b = AssToVTT(b)
			}
			return "", b, nil
		case format == "vtt":
			return "", []byte("WEBVTT\n\n"), nil
		}
		// The ASS header arrives with the first subtitle event.
		select {
		case <-e.done:
			if e.err != nil {
				return "", nil, e.err
			}
			return finish()
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// assVTT converts an extracted ASS track to WebVTT (cached).
func (s *Subs) assVTT(f *store.File, idx int, src string) (string, []byte, error) {
	dst := s.cachePath(f, idx)
	if exists(dst) {
		return dst, nil, nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(dst+".tmp", AssToVTT(b), 0o644); err != nil {
		return "", nil, err
	}
	return dst, nil, os.Rename(dst+".tmp", dst)
}

// cutAt trims b after the last occurrence of sep (drops a half-written tail).
func cutAt(b []byte, sep string) []byte {
	if i := bytes.LastIndex(b, []byte(sep)); i >= 0 {
		return b[:i+len(sep)]
	}
	return nil
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
		// Styled subtitles are copied verbatim (for libass rendering); their
		// WebVTT fallback is derived from that copy by AssToVTT. ffmpeg's own
		// ASS->WebVTT conversion takes over a minute per track on a Pi for
		// heavily typeset releases.
		if IsASS(st.Codec) {
			if ass := s.cachePathExt(f, st.Index, "ass"); !exists(ass) {
				outs = append(outs, out{ass + ".tmp", ass})
				// ignore_readorder writes events as they're demuxed (in time
				// order) so a partial file is usable while extraction runs.
				args = append(args, "-map", fmt.Sprintf("0:%d", st.Index), "-c:s", "copy", "-f", "ass", "-ignore_readorder", "1", "-flush_packets", "1", "-y", ass+".tmp")
			}
			continue
		}
		dst := s.cachePath(f, st.Index)
		if !exists(dst) {
			tmp := dst + ".tmp"
			outs = append(outs, out{tmp, dst})
			args = append(args, "-map", fmt.Sprintf("0:%d", st.Index), "-c:s", "webvtt", "-f", "webvtt", "-flush_packets", "1", "-y", tmp)
		}
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
