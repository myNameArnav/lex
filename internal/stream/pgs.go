package stream

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"lex/internal/proc"
	"lex/internal/store"
)

// PGS (Blu-ray) subtitles are bitmaps, so they can't become WebVTT. Instead
// of burning them into a transcode, the player can draw them over the video
// (web/static/js/pgs.js) from the track copied out as a .sup file, which
// costs the server a demux and keeps playback Direct Play / Direct Stream.

// IsPGS reports whether a subtitle codec is PGS.
func IsPGS(codec string) bool { return codec == "hdmv_pgs_subtitle" }

// ServeSup returns embedded PGS track idx as a .sup file. Copying it out
// reads the whole media file; if that's still running after wait, ServeSup
// returns the display sets copied so far as data (partial), and the player
// asks again later. Otherwise it returns the finished file's path, or no
// path and no data for a track without a single subtitle.
func (s *Subs) ServeSup(ctx context.Context, f *store.File, idx int, wait time.Duration) (path string, data []byte, partial bool, err error) {
	if f.Info == nil {
		return "", nil, false, fmt.Errorf("file not probed")
	}
	st := f.Info.StreamByIndex(idx)
	if st == nil || st.Type != "subtitle" || st.External {
		return "", nil, false, fmt.Errorf("subtitle %d not found", idx)
	}
	if !IsPGS(st.Codec) {
		return "", nil, false, fmt.Errorf("subtitle %d is %s, not PGS", idx, st.Codec)
	}
	dst := s.cachePathExt(f, idx, "sup")
	if exists(dst) {
		return dst, nil, false, nil
	}
	e := s.start(fmt.Sprintf("sup-%d-%d", f.ID, idx), func() error { return s.extractSup(f, idx, dst) })
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-e.done:
		if e.err != nil {
			return "", nil, false, e.err
		}
		if !exists(dst) {
			return "", nil, false, nil // no subtitles at all: nothing to draw
		}
		return dst, nil, false, nil
	case <-ctx.Done():
		return "", nil, false, ctx.Err()
	case <-t.C:
	}
	b, _ := os.ReadFile(dst + ".tmp")
	return "", CompleteDisplaySets(b), true, nil
}

func (s *Subs) extractSup(f *store.File, idx int, dst string) error {
	src := f.Path
	if s.Resolve != nil {
		src = s.Resolve(f)
	}
	tmp := dst + ".tmp"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-i", src,
		"-map", fmt.Sprintf("0:%d", idx), "-c:s", "copy", "-f", "sup", "-flush_packets", "1", "-y", tmp)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	proc.Nice(cmd.Process.Pid, 5)
	if err := cmd.Wait(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("extract PGS track %d: %v: %s", idx, err, strings.TrimSpace(stderr.String()))
	}
	if !exists(tmp) {
		os.Remove(tmp)
		return nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	if s.log != nil {
		s.log.Debugf("extracted PGS track %d of %s in %s", idx, f.Path, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// CompleteDisplaySets returns the longest prefix of a .sup stream that ends
// with a complete display set (an END segment), so a file still being
// written is never cut mid-subtitle. It never returns nil.
func CompleteDisplaySets(b []byte) []byte {
	end := 0
	for off := 0; off+13 <= len(b); {
		if b[off] != 'P' || b[off+1] != 'G' {
			break
		}
		n := off + 13 + int(binary.BigEndian.Uint16(b[off+11:]))
		if n > len(b) {
			break
		}
		if b[off+10] == 0x80 {
			end = n
		}
		off = n
	}
	return append([]byte{}, b[:end]...)
}
