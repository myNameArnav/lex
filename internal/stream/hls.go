package stream

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"lex/internal/logx"
	"lex/internal/proc"
	"lex/internal/store"
)

// HLS serves remuxed/transcoded video as an HLS VOD playlist for clients
// without Media Source Extensions (older iOS, AirPlay). The playlist lists
// fixed-length segments for the whole file; one ffmpeg per session writes
// segments to disk and is restarted at a segment when the player seeks
// outside what's been produced.
const HLSSegment = 6.0

type HLS struct {
	root   string
	ffmpeg string
	log    *logx.Logger
	mu     sync.Mutex
	jobs   map[string]*hlsJob
}

type hlsJob struct {
	key      string
	dir      string
	ext      string
	start    int
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	lastReq  int
	lastSeen time.Time
	paused   bool
	errText  string
	mu       sync.Mutex
}

func NewHLS(root, ffmpeg string, log *logx.Logger) *HLS {
	os.RemoveAll(root)
	os.MkdirAll(root, 0o755)
	h := &HLS{root: root, ffmpeg: ffmpeg, log: log, jobs: map[string]*hlsJob{}}
	go h.janitor()
	return h
}

// SegmentExt: HEVC copy needs fMP4 segments (Apple doesn't allow HEVC in
// MPEG-TS); everything else uses TS, which also suits the Pi's encoder.
func SegmentExt(f *store.File, p Params) string {
	if v := f.Info.Video(); v != nil && p.VideoCopy && v.Codec == "hevc" {
		return "m4s"
	}
	return "ts"
}

// Playlist builds the VOD media playlist. q is the query string (stream
// params + key) appended to every URI.
func Playlist(f *store.File, p Params, q url.Values) string {
	dur := f.Info.Duration
	n := int(math.Ceil(dur / HLSSegment))
	ext := SegmentExt(f, p)
	qs := q.Encode()
	var b strings.Builder
	target := int(HLSSegment)
	if p.VideoCopy {
		// Copied video splits at the source's keyframes: allow long segments.
		target = int(HLSSegment) + 6
	}
	b.WriteString("#EXTM3U\n")
	if ext == "m4s" {
		b.WriteString("#EXT-X-VERSION:7\n")
	} else {
		b.WriteString("#EXT-X-VERSION:3\n")
	}
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n", target)
	if !p.VideoCopy {
		b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	}
	if ext == "m4s" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"init.mp4?%s\"\n", qs)
	}
	for i := 0; i < n; i++ {
		d := HLSSegment
		if i == n-1 {
			d = dur - HLSSegment*float64(n-1)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg/%d.%s?%s\n", d, i, ext, qs)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func jobKey(p Params) string {
	return fmt.Sprintf("%s|%d|%v|%v|%d|%d|%d|%d|%d|%v", p.SessionID, p.FileID, p.VideoCopy, p.AudioCopy, p.Audio, p.Burn, p.Bitrate, p.Height, p.Channels, p.SW)
}

func (j *hlsJob) segPath(n int) string { return filepath.Join(j.dir, fmt.Sprintf("%d.%s", n, j.ext)) }

func (j *hlsJob) running() bool {
	if j.done == nil {
		return false
	}
	select {
	case <-j.done:
		return false
	default:
		return true
	}
}

// produced returns the highest complete segment number, or -1.
func (j *hlsJob) produced() int {
	best := -1
	ents, _ := os.ReadDir(j.dir)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, "."+j.ext) {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSuffix(name, "."+j.ext)); err == nil && n > best {
			best = n
		}
	}
	return best
}

func (j *hlsJob) resume() {
	if j.paused && j.cmd != nil && j.cmd.Process != nil {
		j.cmd.Process.Signal(syscall.SIGCONT)
		j.paused = false
	}
}

func (j *hlsJob) stop() {
	if j.cancel != nil {
		j.resume()
		j.cancel()
		select {
		case <-j.done:
		case <-time.After(3 * time.Second):
		}
	}
}

// Segment returns the path of segment n (or the init segment when n < 0),
// starting or restarting ffmpeg as needed.
func (h *HLS) Segment(ctx context.Context, cfg store.Config, ff FFInfo, f *store.File, p Params, n int, nice int) (string, error) {
	key := jobKey(p)
	h.mu.Lock()
	j := h.jobs[key]
	if j == nil {
		// Parameters changed (quality/audio switch): drop the old job for this session.
		for k, o := range h.jobs {
			if strings.HasPrefix(k, p.SessionID+"|") {
				go func(o *hlsJob) { o.stop(); os.RemoveAll(o.dir) }(o)
				delete(h.jobs, k)
			}
		}
		j = &hlsJob{key: key, dir: filepath.Join(h.root, fmt.Sprintf("%s-%d", sanitize(p.SessionID), time.Now().UnixNano())), ext: SegmentExt(f, p)}
		h.jobs[key] = j
	}
	h.mu.Unlock()

	j.mu.Lock()
	j.lastSeen = time.Now()
	target := n
	if n < 0 {
		target = j.lastReq
	} else {
		j.lastReq = n
	}
	path := j.segPath(n)
	if n < 0 {
		path = filepath.Join(j.dir, "init.mp4")
	}
	if _, err := os.Stat(path); err == nil {
		j.resume()
		j.mu.Unlock()
		return path, nil
	}
	produced := j.produced()
	if !j.running() || target < j.start || target > produced+3 {
		j.mu.Unlock()
		if err := h.start(j, cfg, ff, f, p, target, nice); err != nil {
			return "", err
		}
		j.mu.Lock()
	}
	j.resume()
	j.mu.Unlock()

	// Wait for ffmpeg to finish the segment.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		j.mu.Lock()
		alive := j.running()
		errText := j.errText
		j.mu.Unlock()
		if !alive {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
			return "", fmt.Errorf("ffmpeg stopped before segment %d: %s", n, errText)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("timed out waiting for segment %d", n)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

func (h *HLS) start(j *hlsJob, cfg store.Config, ff FFInfo, f *store.File, p Params, seg, nice int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running() {
		j.mu.Unlock()
		j.stop()
		j.mu.Lock()
	}
	os.RemoveAll(j.dir)
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		return err
	}
	p.Start = float64(seg) * HLSSegment
	if p.SW {
		cfg.VideoEncoder = "libx264"
	}
	args, err := buildArgs(cfg, ff, f, p)
	if err != nil {
		return err
	}
	// Replace the fMP4 pipe output with the HLS muxer.
	cut := len(args)
	for i, a := range args {
		if a == "-map_metadata" {
			cut = i
			break
		}
	}
	args = args[:cut]
	segType, ext := "mpegts", "ts"
	if j.ext == "m4s" {
		segType, ext = "fmp4", "m4s"
	}
	args = append(args,
		"-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn",
		"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096",
		"-f", "hls", "-hls_time", strconv.Itoa(int(HLSSegment)), "-hls_list_size", "0",
		"-start_number", strconv.Itoa(seg), "-hls_segment_type", segType,
		"-hls_flags", "temp_file", "-hls_segment_filename", filepath.Join(j.dir, "%d."+ext))
	if segType == "fmp4" {
		args = append(args, "-hls_fmp4_init_filename", "init.mp4")
	} else {
		args = append(args, "-muxdelay", "0", "-muxpreload", "0")
	}
	args = append(args, filepath.Join(j.dir, "ffmpeg.m3u8"))
	// buildArgs asks for progress on fd 3; HLS jobs don't report it.
	for i, a := range args {
		if a == "-progress" && i+1 < len(args) {
			args[i+1] = "-"
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	cmd.WaitDelay = 3 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	proc.Nice(cmd.Process.Pid, nice)
	j.cmd, j.cancel, j.start, j.paused, j.errText = cmd, cancel, seg, false, ""
	done := make(chan struct{})
	j.done = done
	go func() {
		err := cmd.Wait()
		j.mu.Lock()
		if err != nil && ctx.Err() == nil {
			j.errText = strings.TrimSpace(stderr.String())
			if j.errText == "" {
				j.errText = err.Error()
			}
			h.log.Warnf("hls ffmpeg: %s", j.errText)
		}
		j.mu.Unlock()
		cancel()
		close(done)
	}()
	h.log.Debugf("hls: %s from segment %d", filepath.Base(f.Path), seg)
	return nil
}

// Stop ends every HLS job for a playback session.
func (h *HLS) Stop(sid string) {
	h.mu.Lock()
	var jobs []*hlsJob
	for k, j := range h.jobs {
		if strings.HasPrefix(k, sid+"|") {
			jobs = append(jobs, j)
			delete(h.jobs, k)
		}
	}
	h.mu.Unlock()
	for _, j := range jobs {
		j.stop()
		os.RemoveAll(j.dir)
	}
}

// ActiveTranscodesExcept counts running HLS jobs that re-encode video,
// ignoring the given session (it's about to be replaced).
func (h *HLS) ActiveTranscodesExcept(sid string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for k, j := range h.jobs {
		parts := strings.Split(k, "|")
		if len(parts) > 2 && parts[0] != sid && parts[2] == "false" && j.running() {
			n++
		}
	}
	return n
}

// janitor throttles ffmpeg when it's far ahead of the player, deletes old
// segments and drops idle jobs.
func (h *HLS) janitor() {
	for range time.Tick(5 * time.Second) {
		h.mu.Lock()
		jobs := make(map[string]*hlsJob, len(h.jobs))
		for k, j := range h.jobs {
			jobs[k] = j
		}
		h.mu.Unlock()
		for k, j := range jobs {
			j.mu.Lock()
			idle := time.Since(j.lastSeen)
			if idle > 3*time.Minute {
				j.mu.Unlock()
				j.stop()
				os.RemoveAll(j.dir)
				h.mu.Lock()
				delete(h.jobs, k)
				h.mu.Unlock()
				continue
			}
			produced := j.produced()
			if j.running() && !j.paused && produced-j.lastReq > int(120/HLSSegment) {
				if j.cmd.Process.Signal(syscall.SIGSTOP) == nil {
					j.paused = true
				}
			} else if j.paused && produced-j.lastReq < int(60/HLSSegment) {
				j.resume()
			}
			// Keep a little history for short rewinds.
			ents, _ := os.ReadDir(j.dir)
			var old []string
			for _, e := range ents {
				if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), "."+j.ext)); err == nil && n < j.lastReq-10 {
					old = append(old, e.Name())
				}
			}
			sort.Strings(old)
			for _, name := range old {
				os.Remove(filepath.Join(j.dir, name))
			}
			j.mu.Unlock()
		}
	}
}
