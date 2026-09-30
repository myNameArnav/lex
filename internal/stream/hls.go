package stream

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/logx"
	"lex/internal/proc"
	"lex/internal/store"
)

// HLS serves remuxed/transcoded video as an HLS VOD playlist for clients
// without Media Source Extensions (older iOS, AirPlay).
//
// The playlist is exact: segment boundaries are fixed before ffmpeg runs,
// and ffmpeg's segment muxer cuts at exactly those times.
//   - Transcodes get a boundary every HLSSegment seconds, and the encoder is
//     forced to start a keyframe on each one.
//   - Copied video can only be cut at the source's keyframes: a boundary is
//     the first keyframe at least HLSSegment after the previous one, from
//     the file's keyframe index (Matroska cues). Files without one are
//     transcoded instead (see CanCopy).
//
// One ffmpeg per session produces the segments in order. It's restarted at
// a segment when the player seeks outside what's been produced, and paused
// while it's far ahead of the player.
const HLSSegment = 6.0

var errHLSStopped = errors.New("playback session ended")

// copySeekPad: ffmpeg seeks a little before -ss (3/23 s for streams with
// B-frames), which would land on the keyframe before a boundary.
const copySeekPad = 0.15

type HLS struct {
	root   string
	ffmpeg string
	log    *logx.Logger
	// Slots enforces the transcode limit together with MSE sessions.
	Slots *Transcodes

	mu   sync.Mutex
	jobs map[string]*hlsJob
	runs int // numbers ffmpeg runs, for transcode slot owners

	kfMu sync.Mutex
	kf   map[string][]float64 // keyframe index per file version; nil = none
}

type hlsJob struct {
	key, sid, dir, ext string
	copy               bool
	bounds             []float64 // segment start times, then the file's end

	mu       sync.Mutex
	removed  bool
	lastReq  int
	lastSeen time.Time
	start    int // first segment of the current ffmpeg run
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	paused   bool

	errMu   sync.Mutex
	errText string // why the current run failed; "" if it didn't
	clean   bool   // the current run reached the end of the input
}

func NewHLS(root, ffmpeg string, log *logx.Logger) *HLS {
	os.RemoveAll(root)
	os.MkdirAll(root, 0o755)
	h := &HLS{root: root, ffmpeg: ffmpeg, log: log, Slots: &Transcodes{}, jobs: map[string]*hlsJob{}}
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

// keyframes returns f's video keyframe times, or nil when the file has no
// usable index. Cached per file version.
func (h *HLS) keyframes(f *store.File) []float64 {
	key := fmt.Sprintf("%s|%d", f.Path, f.Mtime)
	h.kfMu.Lock()
	if t, ok := h.kf[key]; ok {
		h.kfMu.Unlock()
		return t
	}
	h.kfMu.Unlock()
	var times []float64
	switch strings.ToLower(filepath.Ext(f.Path)) {
	case ".mkv", ".mk3d", ".webm":
		if t, err := MatroskaKeyframes(f.Path); err == nil && len(t) >= 2 {
			times = t
		}
	}
	h.kfMu.Lock()
	if h.kf == nil || len(h.kf) > 512 {
		h.kf = map[string][]float64{}
	}
	h.kf[key] = times
	h.kfMu.Unlock()
	return times
}

// CanCopy reports whether HLS can remux f's video: it needs keyframe times
// to cut at, which only Matroska files index.
func (h *HLS) CanCopy(f *store.File) bool { return h.keyframes(f) != nil }

// bounds returns the segment start times followed by the end of the file.
func (h *HLS) bounds(f *store.File, p Params) []float64 {
	dur := f.Info.Duration
	b := []float64{0}
	if p.VideoCopy {
		if kf := h.keyframes(f); kf != nil {
			for _, t := range kf {
				if t >= b[len(b)-1]+HLSSegment && t < dur-0.5 {
					b = append(b, t)
				}
			}
			return append(b, dur)
		}
	}
	for i := 1; float64(i)*HLSSegment < dur-0.5; i++ {
		b = append(b, float64(i)*HLSSegment)
	}
	return append(b, dur)
}

// Playlist builds the VOD media playlist. q is the query string (stream
// params + key) appended to every URI.
func (h *HLS) Playlist(f *store.File, p Params, q url.Values) string {
	b := h.bounds(f, p)
	ext := SegmentExt(f, p)
	qs := q.Encode()
	longest := 0.0
	for i := 1; i < len(b); i++ {
		longest = math.Max(longest, b[i]-b[i-1])
	}
	var s strings.Builder
	s.WriteString("#EXTM3U\n")
	if ext == "m4s" {
		s.WriteString("#EXT-X-VERSION:7\n")
	} else {
		s.WriteString("#EXT-X-VERSION:3\n")
	}
	fmt.Fprintf(&s, "#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n", int(math.Ceil(longest)))
	if ext == "m4s" {
		fmt.Fprintf(&s, "#EXT-X-MAP:URI=\"init.mp4?%s\"\n", qs)
	}
	for i := 0; i+1 < len(b); i++ {
		fmt.Fprintf(&s, "#EXTINF:%.3f,\nseg/%d.%s?%s\n", b[i+1]-b[i], i, ext, qs)
	}
	s.WriteString("#EXT-X-ENDLIST\n")
	return s.String()
}

func jobKey(p Params) string {
	return fmt.Sprintf("%s|%d|%v|%v|%d|%d|%d|%d|%d|%v", p.SessionID, p.FileID, p.VideoCopy, p.AudioCopy, p.Audio, p.Burn, p.Bitrate, p.Height, p.Channels, p.SW)
}

var validSID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

// job returns the job for these parameters, replacing the session's jobs
// with other parameters (quality or audio switch).
func (h *HLS) job(f *store.File, p Params) *hlsJob {
	key := jobKey(p)
	h.mu.Lock()
	if j := h.jobs[key]; j != nil {
		h.mu.Unlock()
		return j
	}
	h.mu.Unlock()
	b := h.bounds(f, p) // may read the file's index: outside the lock
	h.mu.Lock()
	defer h.mu.Unlock()
	if j := h.jobs[key]; j != nil {
		return j
	}
	for _, o := range h.jobs {
		if o.sid == p.SessionID {
			h.dropLocked(o)
		}
	}
	j := &hlsJob{key: key, sid: p.SessionID, dir: filepath.Join(h.root, fmt.Sprintf("%s-%d", p.SessionID, time.Now().UnixNano())),
		ext: SegmentExt(f, p), copy: p.VideoCopy, bounds: b, lastSeen: time.Now()}
	h.jobs[key] = j
	return j
}

// dropLocked removes a job and stops its ffmpeg (h.mu held). Requests
// waiting on it get errHLSStopped, and it can't be started again.
func (h *HLS) dropLocked(j *hlsJob) {
	if h.jobs[j.key] == j {
		delete(h.jobs, j.key)
	}
	j.mu.Lock()
	j.removed = true
	done := j.stopAsync()
	j.mu.Unlock()
	go func() {
		if done != nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
		os.RemoveAll(j.dir)
	}()
}

// Stop ends every HLS job for a playback session.
func (h *HLS) Stop(sid string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, j := range h.jobs {
		if j.sid == sid {
			h.dropLocked(j)
		}
	}
}

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

func (j *hlsJob) resume() {
	if j.paused && j.cmd != nil {
		proc.Resume(j.cmd.Process)
		j.paused = false
	}
}

// stopAsync cancels the current run (j.mu held) and returns its done channel.
func (j *hlsJob) stopAsync() chan struct{} {
	if j.cancel == nil {
		return nil
	}
	j.resume()
	j.cancel()
	return j.done
}

func (j *hlsJob) runState() (failed string, clean bool) {
	j.errMu.Lock()
	defer j.errMu.Unlock()
	return j.errText, j.clean
}

// raw is where ffmpeg writes segment n; for fMP4 it's a self-contained MP4
// that's split into the shared init segment and the media segment on use.
func (j *hlsJob) raw(n int) string {
	if j.ext == "m4s" {
		return filepath.Join(j.dir, fmt.Sprintf("%d.mp4", n))
	}
	return filepath.Join(j.dir, fmt.Sprintf("%d.ts", n))
}

func (j *hlsJob) served(n int) string {
	if n < 0 {
		return filepath.Join(j.dir, "init.mp4")
	}
	return filepath.Join(j.dir, fmt.Sprintf("%d.%s", n, j.ext))
}

// listed returns the segments ffmpeg has finished (from its segment list).
func (j *hlsJob) listed() map[int]bool {
	out := map[int]bool{}
	f, err := os.Open(filepath.Join(j.dir, "list.csv"))
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, _, _ := strings.Cut(sc.Text(), ",")
		if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(name, ".ts"), ".mp4")); err == nil {
			out[n] = true
		}
	}
	return out
}

// ready makes segment n (or the init segment, n < 0) servable if ffmpeg has
// finished it, returning its path.
func (j *hlsJob) ready(n int, done map[int]bool) (string, bool) {
	if n >= 0 && !done[n] {
		return "", false
	}
	if j.ext != "m4s" {
		p := j.served(n)
		_, err := os.Stat(p)
		return p, err == nil
	}
	if n < 0 {
		p := j.served(-1)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		for m := range done {
			if split(j.raw(m), p, "") == nil {
				return p, true
			}
		}
		return "", false
	}
	p := j.served(n)
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	init := j.served(-1)
	if _, err := os.Stat(init); err == nil {
		init = ""
	}
	if err := split(j.raw(n), init, p); err != nil {
		return "", false
	}
	return p, true
}

// split cuts a fragmented MP4 into its init segment (ftyp+moov, written to
// init unless it's "") and its media segment (the rest, written to media
// unless it's "").
func split(src, init, media string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	off := 0
	for off+8 <= len(b) {
		size := int(binary.BigEndian.Uint32(b[off:]))
		if size < 8 || off+size > len(b) {
			return errors.New("bad MP4 box")
		}
		kind := string(b[off+4 : off+8])
		off += size
		if kind == "moov" {
			if init != "" {
				if err := writeAtomic(init, b[:off]); err != nil {
					return err
				}
			}
			if media != "" {
				return writeAtomic(media, b[off:])
			}
			return nil
		}
	}
	return errors.New("no moov box")
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".part-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), path)
}

// Segment returns the path of segment n (or the init segment when n < 0),
// starting or restarting ffmpeg as needed.
func (h *HLS) Segment(ctx context.Context, cfg store.Config, ff FFInfo, f *store.File, p Params, n int, nice int) (string, error) {
	if !validSID.MatchString(p.SessionID) {
		return "", errors.New("missing or invalid session")
	}
	j := h.job(f, p)
	if n >= len(j.bounds)-1 {
		return "", fmt.Errorf("segment %d is past the end", n)
	}
	deadline := time.Now().Add(90 * time.Second)
	j.mu.Lock()
	defer j.mu.Unlock()
	target := n
	if n < 0 {
		target = max(j.lastReq, 0)
	} else {
		j.lastReq = n
	}
	for {
		if j.removed {
			return "", errHLSStopped
		}
		j.lastSeen = time.Now()
		done := j.listed()
		if path, ok := j.ready(n, done); ok {
			j.resume()
			return path, nil
		}
		if n < 0 && len(done) > 0 {
			// Finished segments exist but none could be split: ffmpeg
			// output is unusable, and waiting won't help.
			return "", errors.New("could not read the init segment")
		}
		produced := -1
		for m := range done {
			produced = max(produced, m)
		}
		failed, clean := j.runState()
		running := j.running()
		switch {
		case !running && failed != "" && j.start == target:
			return "", fmt.Errorf("ffmpeg failed at segment %d: %s", target, failed)
		case !running && clean && target >= j.start && target > produced:
			return "", fmt.Errorf("segment %d is past the end of the media", target)
		case !running || target < j.start || target > max(produced, j.start-1)+3 || (target <= produced && !done[target]):
			// Not running, a seek outside what this run will produce soon,
			// or a segment already deleted to save space: (re)start here.
			if err := h.startLocked(j, cfg, ff, f, p, target, nice); err != nil {
				return "", err
			}
		}
		j.resume()
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for segment %d", n)
		}
		j.mu.Unlock()
		select {
		case <-ctx.Done():
			j.mu.Lock()
			return "", ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		j.mu.Lock()
	}
}

// startLocked runs ffmpeg from segment seg (j.mu held).
func (h *HLS) startLocked(j *hlsJob, cfg store.Config, ff FFInfo, f *store.File, p Params, seg, nice int) error {
	if done := j.stopAsync(); done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	j.cmd, j.cancel, j.done, j.paused = nil, nil, nil, false
	slot := ""
	if !j.copy {
		h.mu.Lock()
		h.runs++
		slot = fmt.Sprintf("hls:%s:%d", j.sid, h.runs)
		h.mu.Unlock()
		if !h.Slots.Acquire(slot, cfg.MaxTranscodes) {
			return ErrLimit
		}
	}
	release := func() {
		if slot != "" {
			h.Slots.Release(slot)
		}
	}
	os.RemoveAll(j.dir)
	if err := os.MkdirAll(j.dir, 0o755); err != nil {
		release()
		return err
	}
	pp := p
	pp.Start = j.bounds[seg]
	if j.copy {
		pp.Start += copySeekPad
	}
	if p.SW {
		cfg.VideoEncoder = "libx264"
	}
	args, err := buildArgs(cfg, ff, f, pp)
	if err != nil {
		release()
		return err
	}
	// Keep the input side, replace the fMP4 pipe output with the segmenter.
	cut := len(args)
	for i, a := range args {
		if a == "-map_metadata" {
			cut = i
			break
		}
	}
	args = args[:cut]
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-progress" { // HLS jobs don't report progress
			args = append(args[:i], args[i+2:]...)
			break
		}
	}
	if j.copy {
		// Start at the boundary keyframe itself, and don't trim decoded
		// streams (audio) to the padded seek point.
		for i, a := range args {
			if a == "-ss" {
				args = append(args[:i], append([]string{"-noaccurate_seek"}, args[i:]...)...)
				break
			}
		}
	} else {
		// A keyframe on every boundary (t counts from this run's start,
		// which is itself a boundary).
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%g)", HLSSegment))
	}
	// Cut times are relative to the first packet: this run's boundary.
	var times []string
	for _, b := range j.bounds[seg+1 : len(j.bounds)-1] {
		times = append(times, strconv.FormatFloat(b-j.bounds[seg]-0.001, 'f', 3, 64))
	}
	args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn",
		"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096",
		"-f", "segment", "-segment_start_number", strconv.Itoa(seg),
		"-segment_list", filepath.Join(j.dir, "list.csv"), "-segment_list_type", "csv", "-reset_timestamps", "0")
	if len(times) > 0 {
		args = append(args, "-segment_times", strings.Join(times, ","))
	} else {
		args = append(args, "-segment_time", "100000")
	}
	if j.ext == "m4s" {
		args = append(args, "-segment_format", "mp4", "-segment_format_options", "movflags=+frag_keyframe+empty_moov+default_base_moof")
	} else {
		args = append(args, "-segment_format", "mpegts", "-muxdelay", "0", "-muxpreload", "0")
	}
	args = append(args, strings.TrimSuffix(j.raw(seg), strconv.Itoa(seg)+filepath.Ext(j.raw(seg)))+"%d"+filepath.Ext(j.raw(seg)))
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, h.ffmpeg, args...)
	cmd.WaitDelay = 3 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		release()
		return err
	}
	proc.Nice(cmd.Process.Pid, nice)
	j.errMu.Lock()
	j.errText, j.clean = "", false
	j.errMu.Unlock()
	done := make(chan struct{})
	j.cmd, j.cancel, j.done, j.start = cmd, cancel, done, seg
	go func() {
		err := cmd.Wait()
		// Not j.mu: startLocked and dropLocked wait for done while holding it.
		j.errMu.Lock()
		switch {
		case err == nil:
			j.clean = true
		case ctx.Err() == nil:
			j.errText = strings.TrimSpace(stderr.String())
			if j.errText == "" {
				j.errText = err.Error()
			}
			h.log.Warnf("hls ffmpeg: %s", j.errText)
		}
		j.errMu.Unlock()
		cancel()
		release()
		close(done)
	}()
	h.log.Debugf("hls: %s from segment %d (%s)", filepath.Base(f.Path), seg, map[bool]string{true: "copy", false: "transcode"}[j.copy])
	return nil
}

// janitor pauses ffmpeg when it's far ahead of the player, deletes old
// segments and drops idle jobs.
func (h *HLS) janitor() {
	for range time.Tick(5 * time.Second) {
		h.mu.Lock()
		jobs := make([]*hlsJob, 0, len(h.jobs))
		for _, j := range h.jobs {
			jobs = append(jobs, j)
		}
		h.mu.Unlock()
		for _, j := range jobs {
			j.mu.Lock()
			if j.removed {
				j.mu.Unlock()
				continue
			}
			if time.Since(j.lastSeen) > 3*time.Minute {
				j.mu.Unlock()
				h.mu.Lock()
				h.dropLocked(j)
				h.mu.Unlock()
				continue
			}
			done := j.listed()
			produced := -1
			for n := range done {
				produced = max(produced, n)
			}
			ahead := produced - j.lastReq
			if j.running() && !j.paused && ahead > int(120/HLSSegment) {
				j.paused = proc.Suspend(j.cmd.Process)
			} else if j.paused && ahead < int(60/HLSSegment) {
				j.resume()
			}
			// Keep a little history for short rewinds.
			for n := range done {
				if n < j.lastReq-10 {
					os.Remove(j.raw(n))
					os.Remove(j.served(n))
				}
			}
			j.mu.Unlock()
		}
	}
}
