package stream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/proc"
	"lex/internal/store"
)

// FFInfo describes the ffmpeg build.
type FFInfo struct {
	Path          string   `json:"path"`
	Probe         string   `json:"probe"`
	Version       string   `json:"version"`
	Encoders      []string `json:"encoders"` // relevant h264 encoders found
	HasZscale     bool     `json:"hasZscale"`
	HasTonemap    bool     `json:"hasTonemap"`
	V4L2Device    bool     `json:"v4l2Device"`   // hardware H.264 encoder works
	HEVCHWDecode  bool     `json:"hevcHwDecode"` // Raspberry Pi 4/5 stateless HEVC decoder (rpivid)
	HasDRMHwaccel bool     `json:"hasDrmHwaccel"`
}

func DetectFFmpeg(ffmpeg, ffprobe string) FFInfo {
	fi := FFInfo{Path: ffmpeg, Probe: ffprobe}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-version").Output(); err == nil {
		line, _, _ := strings.Cut(string(out), "\n")
		fi.Version = strings.TrimSpace(strings.TrimPrefix(line, "ffmpeg version "))
		if i := strings.Index(fi.Version, " Copyright"); i > 0 {
			fi.Version = fi.Version[:i]
		}
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output(); err == nil {
		for _, enc := range []string{"libx264", "h264_v4l2m2m", "h264_omx", "h264_vaapi", "h264_videotoolbox"} {
			if bytes.Contains(out, []byte(" "+enc+" ")) {
				fi.Encoders = append(fi.Encoders, enc)
			}
		}
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-filters").Output(); err == nil {
		fi.HasZscale = bytes.Contains(out, []byte(" zscale "))
		fi.HasTonemap = bytes.Contains(out, []byte(" tonemap "))
	}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-hwaccels").Output(); err == nil {
		fi.HasDRMHwaccel = bytes.Contains(out, []byte("\ndrm"))
	}
	if _, err := os.Stat("/dev/video19"); err == nil && fi.HasDRMHwaccel {
		fi.HEVCHWDecode = true
	}
	// Only trust the hardware encoder after a real (tiny) test encode.
	if _, err := os.Stat("/dev/video11"); err == nil && slices.Contains(fi.Encoders, "h264_v4l2m2m") {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25",
			"-frames:v", "10", "-pix_fmt", "yuv420p", "-c:v", "h264_v4l2m2m", "-b:v", "1M", "-f", "null", "-").Run()
		cancel()
		fi.V4L2Device = err == nil
	}
	return fi
}

// Progress is parsed from ffmpeg's -progress output.
type Progress struct {
	Frame      int64   `json:"frame"`
	FPS        float64 `json:"fps"`
	Speed      float64 `json:"speed"`
	OutTime    float64 `json:"outTime"`
	Bitrate    string  `json:"bitrate"`
	TotalSize  int64   `json:"totalSize"`
	Updated    int64   `json:"updated"` // unix ms
	CPUPercent float64 `json:"cpu"`
	Throttled  bool    `json:"throttled"`
	Exited     bool    `json:"exited"`
	Error      string  `json:"error,omitempty"`
}

type Job struct {
	slot    string // transcode slot held until ffmpeg exits
	Params  Params
	Args    []string
	Started time.Time
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	Stdout  io.ReadCloser

	mu       sync.Mutex
	prog     Progress
	stderr   []string
	done     chan struct{}
	lastCPU  float64
	lastCPUt time.Time
}

func (j *Job) Progress() Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := j.prog
	// When the client's buffer is full, ffmpeg blocks on the pipe and stops
	// reporting progress: that's throttling, not a stall.
	if !p.Exited && p.Updated > 0 && time.Since(time.UnixMilli(p.Updated)) > 2*time.Second {
		p.Throttled = true
		p.Speed = 0
		p.FPS = 0
	}
	return p
}

func (j *Job) Pid() int {
	if j.cmd != nil && j.cmd.Process != nil {
		return j.cmd.Process.Pid
	}
	return 0
}

func (j *Job) Done() <-chan struct{} { return j.done }

func (j *Job) Stop() {
	j.cancel()
}

func (j *Job) StderrTail() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return strings.Join(j.stderr, "\n")
}

// SampleCPU updates the job's CPU% from /proc (Linux only).
func (j *Job) SampleCPU() {
	pid := j.Pid()
	if pid == 0 {
		return
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i > 0 {
		s = s[i+2:]
	}
	f := strings.Fields(s)
	if len(f) < 13 {
		return
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	secs := (ut + st) / 100 // USER_HZ
	now := time.Now()
	j.mu.Lock()
	if !j.lastCPUt.IsZero() {
		dt := now.Sub(j.lastCPUt).Seconds()
		if dt > 0 {
			j.prog.CPUPercent = (secs - j.lastCPU) / dt * 100
		}
	}
	j.lastCPU, j.lastCPUt = secs, now
	j.mu.Unlock()
}

// Command is one or two chained ffmpeg invocations. The Raspberry Pi's
// h264_v4l2m2m encoder emits Annex-B without the extradata the MP4 muxer
// needs, so its output goes through MPEG-TS and a second, copy-only ffmpeg
// builds the fragmented MP4 (the TS demuxer recovers SPS/PPS).
type Command struct {
	Args   []string
	Stage2 []string
}

// HWEncode reports whether a transcode uses the V4L2 hardware encoder.
func HWEncode(cfg store.Config, p Params) bool {
	return !p.VideoCopy && !p.SW && cfg.VideoEncoder == "h264_v4l2m2m"
}

// BuildArgs creates the ffmpeg command line(s) for a job.
func BuildArgs(cfg store.Config, ff FFInfo, f *store.File, p Params) (Command, error) {
	if p.SW {
		cfg.VideoEncoder = "libx264"
	}
	args, err := buildArgs(cfg, ff, f, p)
	if err != nil || !HWEncode(cfg, p) {
		return Command{Args: args}, err
	}
	// Swap the MP4 output for MPEG-TS and add the remux stage.
	mp4 := len(args)
	for i, a := range args {
		if a == "-f" && i+1 < len(args) && args[i+1] == "mp4" {
			mp4 = i
			break
		}
	}
	out := append([]string{}, args[:mp4]...)
	out = append(out, "-f", "mpegts", "-mpegts_copyts", "1", "pipe:1")
	stage2 := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-copyts", "-f", "mpegts", "-i", "pipe:0", "-map", "0", "-c", "copy"}
	if a := f.Info.StreamByIndex(p.Audio); a != nil && (!p.AudioCopy || a.Codec == "aac") {
		stage2 = append(stage2, "-bsf:a", "aac_adtstoasc")
	}
	// delay_moov: the AAC AudioSpecificConfig only exists once
	// aac_adtstoasc has seen a packet; writing the moov earlier leaves an
	// esds without it, which Chrome's MSE parser rejects.
	stage2 = append(stage2, "-avoid_negative_ts", "disabled",
		"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+delay_moov+default_base_moof+frag_discont",
		"-min_frag_duration", strconv.Itoa(cfg.FragmentMs*1000), "pipe:1")
	return Command{Args: out, Stage2: stage2}, nil
}

func buildArgs(cfg store.Config, ff FFInfo, f *store.File, p Params) ([]string, error) {
	info := f.Info
	if info == nil {
		return nil, fmt.Errorf("file not probed")
	}
	v := info.Video()
	if v == nil {
		return nil, fmt.Errorf("no video stream")
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-nostats", "-progress", "pipe:3", "-stats_period", "1"}
	if !p.VideoCopy {
		threads := cfg.TranscodeThreads
		if threads > 0 {
			args = append(args, "-threads", strconv.Itoa(threads))
		}
	}
	if !p.VideoCopy && p.HW && v.Codec == "hevc" {
		// Hardware HEVC decode; frames come back to system memory, so the
		// rest of the software filter chain is unchanged.
		args = append(args, "-hwaccel", "drm")
	}
	if p.Start > 0 {
		args = append(args, "-ss", strconv.FormatFloat(p.Start, 'f', 3, 64))
	}
	input := f.Path
	if p.Input != "" {
		input = p.Input
	}
	// Keep source timestamps so the player's timeline equals the file's.
	args = append(args, "-copyts", "-fflags", "+genpts", "-i", input)
	if !p.VideoCopy && p.Burn >= 0 {
		// Subtitles to burn in come from a second demuxer on the same file:
		// sharing one demuxer with the audio stream roughly halves overlay
		// throughput (ffmpeg's sub2video scheduling stalls on interleaving).
		if p.Start > 0 {
			args = append(args, "-ss", strconv.FormatFloat(p.Start, 'f', 3, 64))
		}
		args = append(args, "-copyts", "-i", input)
	}

	var audio *store.Stream
	if p.Audio >= 0 {
		audio = info.StreamByIndex(p.Audio)
		if audio == nil || audio.Type != "audio" {
			audio = nil
		}
	}

	if p.VideoCopy {
		args = append(args, "-map", fmt.Sprintf("0:%d", v.Index), "-c:v", "copy")
		if v.Codec == "hevc" {
			args = append(args, "-tag:v", "hvc1")
		}
	} else {
		var filters []string
		h := p.Height
		if h <= 0 {
			h = 720
		}
		needScale := v.Height > h
		hdr := v.HDR != ""
		if needScale {
			filters = append(filters, fmt.Sprintf("scale=-2:%d:flags=fast_bilinear", h))
		}
		if hdr && cfg.Tonemap && ff.HasZscale && ff.HasTonemap {
			filters = append(filters, "zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709", "tonemap=tonemap=hable:desat=0", "zscale=t=bt709:m=bt709:r=tv")
		}
		filters = append(filters, "format=yuv420p")
		chain := strings.Join(filters, ",")
		if p.Burn >= 0 {
			// Scale first, then overlay subtitles scaled to match: overlaying
			// full-size 10-bit frames is much slower on small CPUs.
			args = append(args, "-filter_complex", fmt.Sprintf("[0:%d]%s[v0];[1:%d][v0]scale2ref[s][v1];[v1][s]overlay=eof_action=pass:repeatlast=0:format=yuv420[v]", v.Index, chain, p.Burn), "-map", "[v]")
		} else {
			args = append(args, "-map", fmt.Sprintf("0:%d", v.Index), "-vf", chain)
		}
		fps := v.FrameRate
		if fps <= 0 || fps > 120 {
			fps = 24
		}
		gop := int(fps*float64(cfg.KeyframeSec) + 0.5)
		br := p.Bitrate
		if br <= 0 {
			br = 4000
		}
		switch cfg.VideoEncoder {
		case "h264_v4l2m2m":
			args = append(args, "-c:v", "h264_v4l2m2m", "-b:v", fmt.Sprintf("%dk", br), "-g", strconv.Itoa(gop), "-bf", "0", "-num_capture_buffers", "32")
		default:
			args = append(args, "-c:v", "libx264", "-preset", cfg.X264Preset, "-crf", strconv.Itoa(cfg.X264CRF),
				"-maxrate", fmt.Sprintf("%dk", br), "-bufsize", fmt.Sprintf("%dk", br*2),
				"-profile:v", "high", "-level:v", h264Level(h), "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop),
				"-sc_threshold", "0", "-x264-params", "rc-lookahead=10:ref=2")
		}
	}
	if audio != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", audio.Index))
		if p.AudioCopy {
			args = append(args, "-c:a", "copy")
		} else {
			ch := p.Channels
			if ch <= 0 {
				ch = 2
			}
			args = append(args, "-c:a", "aac", "-ac", strconv.Itoa(ch), "-b:a", fmt.Sprintf("%dk", AudioBitrate(cfg, ch)))
			if audio.SampleRate > 48000 {
				args = append(args, "-ar", "48000")
			}
		}
	}
	// A duration cutoff can split a GOP between dependent frames. Firefox's
	// MSE parser can then discard frames until the next keyframe, leaving a
	// permanent playback hole despite a healthy download. Use the duration
	// as a minimum and let frag_keyframe choose every fragment boundary.
	args = append(args,
		"-map_metadata", "-1", "-map_chapters", "-1", "-sn", "-dn",
		"-avoid_negative_ts", "disabled", "-max_muxing_queue_size", "4096",
		"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof+frag_discont",
		"-min_frag_duration", strconv.Itoa(cfg.FragmentMs*1000),
		"pipe:1")
	return args, nil
}

// StartJob launches ffmpeg (and the optional remux stage). Stdout carries
// the fragmented MP4.
//
// All pipes are created with os.Pipe so that cmd.Wait (run in a goroutine)
// never closes a read end that a reader is still draining.
func StartJob(ffmpeg string, c Command, p Params, nice int) (*Job, error) {
	args := c.Args
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.WaitDelay = 3 * time.Second
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			files = append(files, r, w)
		}
		return r, w, err
	}
	outR, outW, err := pipe()
	if err != nil {
		cancel()
		return nil, err
	}
	pr, pw, err := pipe()
	if err != nil {
		cancel()
		closeAll()
		return nil, err
	}
	errR, errW, err := pipe()
	if err != nil {
		cancel()
		closeAll()
		return nil, err
	}
	cmd.Stderr = errW
	cmd.ExtraFiles = []*os.File{pw}
	var stage2 *exec.Cmd
	var midR, midW *os.File
	if len(c.Stage2) > 0 {
		if midR, midW, err = pipe(); err != nil {
			cancel()
			closeAll()
			return nil, err
		}
		cmd.Stdout = midW
		stage2 = exec.CommandContext(ctx, ffmpeg, c.Stage2...)
		stage2.WaitDelay = 3 * time.Second
		stage2.Stdin, stage2.Stdout, stage2.Stderr = midR, outW, errW
	} else {
		cmd.Stdout = outW
	}
	j := &Job{Params: p, Args: append(append([]string{}, args...), c.Stage2...), Started: time.Now(), cmd: cmd, cancel: cancel, Stdout: outR, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		cancel()
		closeAll()
		return nil, err
	}
	if stage2 != nil {
		if err := stage2.Start(); err != nil {
			cancel()
			cmd.Wait()
			closeAll()
			return nil, err
		}
		midR.Close()
		midW.Close()
	}
	// The children own the write ends now.
	outW.Close()
	pw.Close()
	errW.Close()
	stderr := errR
	proc.Nice(cmd.Process.Pid, nice)
	j.mu.Lock()
	j.prog.Updated = time.Now().UnixMilli()
	j.prog.OutTime = p.Start
	j.mu.Unlock()

	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			j.mu.Lock()
			switch k {
			case "frame":
				j.prog.Frame, _ = strconv.ParseInt(v, 10, 64)
			case "fps":
				j.prog.FPS, _ = strconv.ParseFloat(v, 64)
			case "speed":
				j.prog.Speed, _ = strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "x"), 64)
			case "out_time_us":
				if us, err := strconv.ParseInt(v, 10, 64); err == nil && us > 0 {
					// Relative to the seek point, even with -copyts.
					j.prog.OutTime = p.Start + float64(us)/1e6
				}
			case "bitrate":
				j.prog.Bitrate = strings.TrimSpace(v)
			case "total_size":
				j.prog.TotalSize, _ = strconv.ParseInt(v, 10, 64)
			case "progress":
				j.prog.Updated = time.Now().UnixMilli()
			}
			j.mu.Unlock()
		}
		pr.Close()
	}()
	go func() {
		defer stderr.Close()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			j.mu.Lock()
			j.stderr = append(j.stderr, line)
			if len(j.stderr) > 30 {
				j.stderr = j.stderr[len(j.stderr)-30:]
			}
			j.mu.Unlock()
		}
	}()
	go func() {
		err := cmd.Wait()
		if stage2 != nil {
			// Stage 1 closing its stdout lets stage 2 finish the MP4.
			if err2 := stage2.Wait(); err == nil {
				err = err2
			}
		}
		j.mu.Lock()
		j.prog.Exited = true
		if err != nil && ctx.Err() == nil {
			j.prog.Error = err.Error()
		}
		j.mu.Unlock()
		cancel()
		close(j.done)
	}()
	return j, nil
}
