package stream

import (
	"bytes"
	"context"
	"encoding/json"
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
	dir     string
	ffmpeg  string
	ffprobe string
	log     *logx.Logger
	mu      sync.Mutex
	flight  map[string]*extraction
	// Resolve maps a file to the path to read (e.g. an SSD cache copy).
	Resolve func(*store.File) string
}

type extraction struct {
	done chan struct{}
	err  error
}

func NewSubs(dir, ffmpeg, ffprobe string, log *logx.Logger) *Subs {
	os.MkdirAll(dir, 0o755)
	return &Subs{dir: dir, ffmpeg: ffmpeg, ffprobe: ffprobe, log: log, flight: map[string]*extraction{}}
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

// Forget removes the cached conversions of subtitle idx of a file (for every
// version of the file), e.g. when a downloaded subtitle is deleted.
func (s *Subs) Forget(fileID int64, idx int) {
	ents, _ := os.ReadDir(s.dir)
	for _, e := range ents {
		for _, ext := range []string{"vtt", "ass"} {
			if ok, _ := filepath.Match(fmt.Sprintf("%d-*-%d.%s", fileID, idx, ext), e.Name()); ok {
				os.Remove(filepath.Join(s.dir, e.Name()))
			}
		}
	}
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
		fonts, err := s.fontAttachments(cctx, src)
		if err != nil {
			return err
		}
		if len(fonts) > 0 {
			// Each font is dumped into the working directory under a name we
			// chose; -t 0 stops right after reading the header.
			args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
			for _, a := range fonts {
				args = append(args, fmt.Sprintf("-dump_attachment:%d", a.index), a.name)
			}
			args = append(args, "-t", "0", "-i", src, "-f", "null", "-")
			cmd := exec.CommandContext(cctx, s.ffmpeg, args...)
			cmd.Dir = dir
			cmd.Run() // ffmpeg exits non-zero when there's no output stream; the dump still happened
		}
		// Drop anything that isn't a font.
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

type fontAttachment struct {
	index int
	name  string
}

// fontAttachments lists the font attachments of src with the file names to
// dump them as. The filename tags come from the media file, so they can't be
// trusted: ffmpeg would write "../../x" or "/abs/path" wherever it points.
func (s *Subs) fontAttachments(ctx context.Context, src string) ([]fontAttachment, error) {
	out, err := exec.CommandContext(ctx, s.ffprobe, "-v", "error", "-select_streams", "t",
		"-show_entries", "stream=index:stream_tags=filename,mimetype", "-of", "json", "-i", src).Output()
	if err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	var probe struct {
		Streams []struct {
			Index int               `json:"index"`
			Tags  map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("list attachments: %w", err)
	}
	var fonts []fontAttachment
	seen := map[string]bool{}
	for _, st := range probe.Streams {
		var filename, mimetype string
		for k, v := range st.Tags {
			switch strings.ToLower(k) {
			case "filename":
				filename = v
			case "mimetype":
				mimetype = v
			}
		}
		name := fontFileName(filename, mimetype)
		if name == "" {
			continue
		}
		// Names must be unique, ignoring case (for case-insensitive disks).
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		for n := 2; seen[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		seen[strings.ToLower(name)] = true
		fonts = append(fonts, fontAttachment{st.Index, name})
	}
	return fonts, nil
}

// fontFileName turns an attachment's filename tag into a safe local file
// name: no directories, no leading dots, only URL-safe characters, a font
// extension and a sane length. It returns "" for attachments that aren't
// fonts.
func fontFileName(tag, mimetype string) string {
	if i := strings.LastIndexAny(tag, `/\`); i >= 0 {
		tag = tag[i+1:]
	}
	var b strings.Builder
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := strings.TrimLeft(b.String(), ".")
	ext := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if !fontExts[ext] {
		m := strings.ToLower(mimetype)
		if !strings.Contains(m, "font") && !strings.Contains(m, "truetype") && !strings.Contains(m, "opentype") && !strings.Contains(m, "sfnt") {
			return ""
		}
		stem, ext = name, ".ttf"
		if strings.Contains(m, "opentype") || strings.Contains(m, "otf") {
			ext = ".otf"
		}
	}
	if len(stem) > 64 {
		stem = stem[:64]
	}
	if stem = strings.Trim(stem, "."); stem == "" {
		stem = "font"
	}
	return stem + ext
}

// WriteFileAtomic writes data to dst through a uniquely named temporary file
// in the same folder, so concurrent writers never share a temp file and
// readers never see a partial dst.
func WriteFileAtomic(dst string, data []byte) error {
	tmp, err := tempFor(dst)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return commit(tmp, dst)
}

// tempFor creates an empty, uniquely named temporary file next to dst.
func tempFor(dst string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return "", err
	}
	f.Close()
	return f.Name(), nil
}

// commit moves a finished temporary file into place. Losing a race to an
// identical conversion is fine.
func commit(tmp, dst string) error {
	os.Chmod(tmp, 0o644)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		if exists(dst) {
			return nil
		}
		return err
	}
	return nil
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
	// Not de-duplicated: concurrent requests may convert the same track.
	if err := WriteFileAtomic(dst, AssToVTT(b)); err != nil {
		return "", nil, err
	}
	return dst, nil, nil
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
	// A failed or timed-out run leaves truncated tracks; caching those would
	// make them look complete forever.
	for _, o := range outs {
		if err == nil && exists(o.tmp) {
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

// subDemuxers pins ffmpeg's input format for known subtitle extensions, so a
// downloaded or sidecar file is never probed as something else.
var subDemuxers = map[string]string{".srt": "srt", ".ass": "ass", ".ssa": "ass", ".vtt": "webvtt"}

func (s *Subs) convertExternal(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == ".vtt" && utf8.Valid(b) {
		return WriteFileAtomic(dst, b)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if !utf8.Valid(b) {
		args = append(args, "-sub_charenc", "CP1252")
	}
	if f := subDemuxers[ext]; f != "" {
		args = append(args, "-f", f)
	}
	tmp, err := tempFor(dst)
	if err != nil {
		return err
	}
	args = append(args, "-i", src, "-c:s", "webvtt", "-f", "webvtt", "-y", tmp)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, s.ffmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("convert subtitle: %v %s", err, strings.TrimSpace(string(out)))
	}
	return commit(tmp, dst)
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
