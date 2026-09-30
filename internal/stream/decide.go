// Package stream decides how a file should be delivered to a client and runs
// the ffmpeg jobs that remux/transcode it.
package stream

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"lex/internal/store"
)

// Caps is what the browser reported it can decode.
type Caps struct {
	MSE         bool            `json:"mse"`
	Direct      map[string]bool `json:"direct"`      // containers playable natively
	Video       map[string]bool `json:"video"`       // MSE (fMP4) video support
	NativeVideo map[string]bool `json:"nativeVideo"` // <video src> support
	Audio       map[string]bool `json:"audio"`       // MSE (fMP4) audio support
	NativeAudio map[string]bool `json:"nativeAudio"`
}

type PlanRequest struct {
	FileID     int64   `json:"fileId"`
	Audio      int     `json:"audio"`      // stream index, -1 = auto
	Subtitle   int     `json:"subtitle"`   // stream index, -1 = none
	Mode       string  `json:"mode"`       // auto | direct | remux | transcode
	MaxBitrate int     `json:"maxBitrate"` // kbps, 0 = original
	AudioLang  string  `json:"audioLang"`
	SWEncode   bool    `json:"swEncode"` // force the software encoder (fallback)
	ForceHLS   bool    `json:"forceHls"` // HLS instead of MSE (AirPlay, testing)
	Caps       Caps    `json:"caps"`
	SessionID  string  `json:"sessionId"`
	Start      float64 `json:"start"`
}

type Plan struct {
	Method    string   `json:"method"` // direct | remux | transcode
	URL       string   `json:"url"`
	Mime      string   `json:"mime,omitempty"`
	Reasons   []string `json:"reasons"`
	VideoCopy bool     `json:"videoCopy"`
	AudioCopy bool     `json:"audioCopy"`
	VideoOut  string   `json:"videoOut"`
	AudioOut  string   `json:"audioOut"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Bitrate   int      `json:"bitrate"` // kbps (target or source)
	Audio     int      `json:"audio"`
	Subtitle  int      `json:"subtitle"`
	BurnSubs  bool     `json:"burnSubs"`
	Duration  float64  `json:"duration"`
	StartTime float64  `json:"startTime"`
	SessionID string   `json:"sessionId"`
	Remote    bool     `json:"remote"`
	LimitKbps int      `json:"limitKbps"`
	HLS       bool     `json:"hls"`
}

// Params are the ffmpeg job parameters, carried in the stream URL.
type Params struct {
	FileID    int64
	SessionID string
	VideoCopy bool
	AudioCopy bool
	Audio     int // stream index, -1 = no audio
	Burn      int // subtitle stream index to burn in, -1 = none
	Bitrate   int // kbps video target when transcoding
	Height    int // max output height when transcoding
	Channels  int
	Start     float64
	SW        bool   // force software encoding (client fallback)
	HW        bool   // use hardware decoding (set by the server, not the URL)
	Input     string // path to read (SSD cache copy or original)
}

func (p Params) Query() url.Values {
	q := url.Values{}
	q.Set("sid", p.SessionID)
	q.Set("vc", b01(p.VideoCopy))
	q.Set("ac", b01(p.AudioCopy))
	q.Set("a", strconv.Itoa(p.Audio))
	q.Set("burn", strconv.Itoa(p.Burn))
	q.Set("br", strconv.Itoa(p.Bitrate))
	q.Set("h", strconv.Itoa(p.Height))
	q.Set("ch", strconv.Itoa(p.Channels))
	if p.SW {
		q.Set("sw", "1")
	}
	return q
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func ParseParams(fileID int64, q url.Values) Params {
	atoi := func(k string, d int) int {
		if v, err := strconv.Atoi(q.Get(k)); err == nil {
			return v
		}
		return d
	}
	start, _ := strconv.ParseFloat(q.Get("t"), 64)
	if start < 0 {
		start = 0
	}
	return Params{
		FileID: fileID, SessionID: q.Get("sid"), VideoCopy: q.Get("vc") == "1", AudioCopy: q.Get("ac") == "1",
		Audio: atoi("a", -1), Burn: atoi("burn", -1), Bitrate: atoi("br", 4000), Height: atoi("h", 720), Channels: atoi("ch", 2), Start: start,
		SW: q.Get("sw") == "1",
	}
}

// VideoKey buckets a video stream into a capability key.
func VideoKey(s *store.Stream) string {
	switch s.Codec {
	case "h264":
		if s.BitDepth > 8 {
			return "h264_10"
		}
		return "h264"
	case "hevc":
		if s.DVProfile == 5 || (s.DVProfile > 0 && s.DVCompat == 0 && s.DVProfile != 8) {
			return "dv5"
		}
		if s.BitDepth > 8 {
			return "hevc10"
		}
		return "hevc"
	case "av1":
		if s.BitDepth > 8 {
			return "av1_10"
		}
		return "av1"
	case "vp9":
		if s.BitDepth > 8 {
			return "vp9_10"
		}
		return "vp9"
	case "vp8":
		return "vp8"
	}
	return s.Codec
}

// ContainerKey maps a file to a container capability key.
func ContainerKey(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v", ".mov":
		return "mp4"
	case ".webm":
		return "webm"
	case ".mkv":
		return "mkv"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// pickAudio chooses the audio stream: explicit request, preferred language,
// default flag, then first.
func pickAudio(info *store.MediaInfo, req PlanRequest) *store.Stream {
	auds := info.Audio()
	if len(auds) == 0 {
		return nil
	}
	if req.Audio >= 0 {
		for i := range auds {
			if auds[i].Index == req.Audio {
				return &auds[i]
			}
		}
	}
	if req.AudioLang != "" {
		for i := range auds {
			if auds[i].Language == req.AudioLang {
				return &auds[i]
			}
		}
	}
	for i := range auds {
		if auds[i].Default {
			return &auds[i]
		}
	}
	return &auds[0]
}

// defaultAudio is what a browser would play natively.
func defaultAudio(info *store.MediaInfo) *store.Stream {
	auds := info.Audio()
	for i := range auds {
		if auds[i].Default {
			return &auds[i]
		}
	}
	if len(auds) > 0 {
		return &auds[0]
	}
	return nil
}

func heightForBitrate(kbps int) int {
	switch {
	case kbps >= 8000:
		return 1080
	case kbps >= 3000:
		return 720
	case kbps >= 1500:
		return 480
	}
	return 360
}

// Decide builds a playback plan.
func Decide(cfg store.Config, f *store.File, req PlanRequest, remote bool) (*Plan, error) {
	info := f.Info
	if info == nil {
		return nil, fmt.Errorf("file has not been analysed yet; try again in a moment")
	}
	v := info.Video()
	if v == nil {
		return nil, fmt.Errorf("file has no video stream")
	}
	a := pickAudio(info, req)
	plan := &Plan{Duration: info.Duration, StartTime: info.StartTime, SessionID: req.SessionID, Remote: remote, Subtitle: req.Subtitle, Audio: -1}
	if a != nil {
		plan.Audio = a.Index
	}

	limit := req.MaxBitrate
	if remote && cfg.RemoteMaxBitrate > 0 && (limit == 0 || limit > cfg.RemoteMaxBitrate) {
		limit = cfg.RemoteMaxBitrate
	}
	plan.LimitKbps = limit
	srcKbps := int(info.Bitrate / 1000)

	var burn *store.Stream
	if req.Subtitle >= 0 {
		if s := info.StreamByIndex(req.Subtitle); s != nil && s.Type == "subtitle" && !s.TextSub {
			burn = s
		}
	}

	mode := req.Mode
	if mode == "" {
		mode = "auto"
	}
	var reasons []string
	vkey := VideoKey(v)
	ckey := ContainerKey(f.Path)

	// ---- direct play ----
	directOK := cfg.EnableDirectPlay
	if mode == "remux" || mode == "transcode" {
		directOK = false
		reasons = append(reasons, "forced "+mode)
	}
	if directOK {
		switch {
		case !req.Caps.Direct[ckey]:
			directOK = false
			reasons = append(reasons, "container "+ckey+" not supported")
		case !req.Caps.NativeVideo[vkey]:
			directOK = false
		case a != nil && !req.Caps.NativeAudio[a.Codec]:
			directOK = false
		case a != nil && defaultAudio(info) != nil && a.Index != defaultAudio(info).Index:
			directOK = false
			reasons = append(reasons, "non-default audio track")
		case limit > 0 && srcKbps > limit:
			directOK = false
		case burn != nil:
			directOK = false
		}
	}
	if directOK || (mode == "direct" && cfg.EnableDirectPlay) {
		plan.Method = "direct"
		plan.URL = fmt.Sprintf("/api/files/%d/direct?sid=%s", f.ID, url.QueryEscape(req.SessionID))
		plan.VideoCopy, plan.AudioCopy = true, true
		plan.VideoOut = describeVideo(v, true, 0, 0)
		if a != nil {
			plan.AudioOut = describeAudio(a, true, 0)
		}
		plan.Width, plan.Height, plan.Bitrate = v.Width, v.Height, srcKbps
		if mode == "direct" && !directOK {
			reasons = append(reasons, "forced direct play")
		}
		plan.Reasons = dedupe(reasons)
		return plan, nil
	}

	// Browsers without Media Source Extensions (older iOS) get HLS; so does
	// anyone who asks for it (AirPlay). Codec support then comes from what
	// the native player can decode.
	hls := !req.Caps.MSE || req.ForceHLS
	videoCaps, audioCaps := req.Caps.Video, req.Caps.Audio
	if hls {
		videoCaps, audioCaps = req.Caps.NativeVideo, req.Caps.NativeAudio
		plan.HLS = true
	}
	if !cfg.EnableRemux && !cfg.EnableTranscode {
		return nil, fmt.Errorf("direct play isn't possible (%s) and remuxing/transcoding are disabled", strings.Join(reasons, ", "))
	}

	// ---- video: copy or transcode ----
	videoCopy := cfg.EnableRemux && mode != "transcode"
	if !videoCaps[vkey] {
		if videoCopy {
			reasons = append(reasons, fmt.Sprintf("video codec %s not supported", vkey))
		}
		videoCopy = false
	}
	if limit > 0 && srcKbps > limit {
		if videoCopy {
			reasons = append(reasons, fmt.Sprintf("bitrate %d kbps over %d kbps limit", srcKbps, limit))
		}
		videoCopy = false
	}
	if burn != nil {
		if videoCopy {
			reasons = append(reasons, "burning in "+burn.Codec+" subtitles")
		}
		videoCopy = false
	}
	if !videoCopy && !cfg.EnableTranscode {
		return nil, fmt.Errorf("video must be transcoded (%s) but transcoding is disabled", strings.Join(reasons, ", "))
	}

	// ---- audio: copy or transcode ----
	audioCopy := false
	channels := cfg.AudioChannels
	if a != nil {
		audioCopy = audioCaps[a.Codec] && a.CodecString != ""
		// MPEG-TS segments (HLS transcodes) can't carry FLAC/ALAC/Vorbis/Opus.
		if hls && audioCopy && (a.Codec == "flac" || a.Codec == "alac" || a.Codec == "vorbis" || a.Codec == "opus") {
			audioCopy = false
		}
		if !audioCopy {
			reasons = append(reasons, fmt.Sprintf("audio codec %s not supported", a.Codec))
		}
		if a.Channels > 0 && a.Channels < channels {
			channels = a.Channels
		}
	}

	// The hardware-encoder path goes through MPEG-TS, which can't carry
	// FLAC/ALAC/Vorbis: convert those to AAC.
	if !videoCopy && cfg.VideoEncoder == "h264_v4l2m2m" && audioCopy && a != nil && (a.Codec == "flac" || a.Codec == "alac" || a.Codec == "vorbis") {
		audioCopy = false
	}
	if req.SWEncode {
		cfg.VideoEncoder = "libx264"
	}
	p := Params{FileID: f.ID, SessionID: req.SessionID, VideoCopy: videoCopy, AudioCopy: audioCopy, Audio: plan.Audio, Burn: -1, Channels: channels, SW: req.SWEncode}
	if burn != nil {
		p.Burn = burn.Index
		plan.BurnSubs = true
	}
	vcodec := v.CodecString
	if videoCopy {
		plan.Width, plan.Height, plan.Bitrate = v.Width, v.Height, srcKbps
		plan.VideoOut = describeVideo(v, true, 0, 0)
	} else {
		br := limit
		if br <= 0 {
			// "Original" quality but must transcode: pick a sane target.
			br = min(max(srcKbps, 4000), 20000)
			if v.Height <= 720 {
				br = min(br, 6000)
			}
		}
		h := min(heightForBitrate(br), cfg.MaxTranscodeHeight)
		if v.Height > 0 && v.Height < h {
			h = v.Height
		}
		h -= h % 2
		p.Bitrate, p.Height = br, h
		w := 0
		if v.Height > 0 {
			w = int(float64(v.Width)*float64(h)/float64(v.Height)) &^ 1
		}
		plan.Width, plan.Height, plan.Bitrate = w, h, br
		vcodec = h264CodecString(h)
		if cfg.VideoEncoder == "h264_v4l2m2m" {
			vcodec = "avc1.640028" // the Pi encoder always signals High@4.0
		}
		plan.VideoOut = fmt.Sprintf("H.264 %dp %s", h, kbpsStr(br))
		if cfg.VideoEncoder == "h264_v4l2m2m" {
			plan.VideoOut += " (hardware)"
		}
	}
	acodec := ""
	if a != nil {
		if audioCopy {
			acodec = a.CodecString
			plan.AudioOut = describeAudio(a, true, 0)
		} else {
			acodec = "mp4a.40.2"
			plan.AudioOut = fmt.Sprintf("AAC %s %d kbps", chStr(channels), AudioBitrate(cfg, channels))
		}
	}
	plan.VideoCopy, plan.AudioCopy = videoCopy, audioCopy
	if videoCopy {
		plan.Method = "remux"
	} else {
		plan.Method = "transcode"
	}
	codecs := vcodec
	if acodec != "" {
		codecs += "," + acodec
	}
	plan.Mime = fmt.Sprintf(`video/mp4; codecs="%s"`, codecs)
	plan.URL = fmt.Sprintf("/api/files/%d/stream?%s", f.ID, p.Query().Encode())
	if hls {
		plan.URL = fmt.Sprintf("/api/files/%d/hls/index.m3u8?%s", f.ID, p.Query().Encode())
		plan.Mime = "application/vnd.apple.mpegurl"
	}
	plan.Reasons = dedupe(reasons)
	return plan, nil
}

func h264CodecString(h int) string {
	switch {
	case h <= 480:
		return "avc1.64001E" // High L3.0
	case h <= 720:
		return "avc1.64001F" // High L3.1
	default:
		return "avc1.640028" // High L4.0
	}
}

func h264Level(h int) string {
	switch {
	case h <= 480:
		return "3.0"
	case h <= 720:
		return "3.1"
	default:
		return "4.0"
	}
}

func AudioBitrate(cfg store.Config, channels int) int {
	if channels > 2 {
		return cfg.AudioBitrate * 2
	}
	return cfg.AudioBitrate
}

func chStr(ch int) string {
	switch ch {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	}
	return fmt.Sprintf("%dch", ch)
}

func kbpsStr(k int) string {
	if k >= 1000 {
		return fmt.Sprintf("%.1f Mbps", float64(k)/1000)
	}
	return fmt.Sprintf("%d kbps", k)
}

func describeVideo(v *store.Stream, copy bool, h, br int) string {
	s := strings.ToUpper(v.Codec)
	if v.Codec == "h264" {
		s = "H.264"
	} else if v.Codec == "hevc" {
		s = "HEVC"
	}
	if v.Height > 0 {
		s += " " + ResLabel(v.Width, v.Height)
	}
	if v.HDR != "" {
		s += " " + v.HDR
	}
	if copy {
		s += " (copy)"
	}
	return s
}

// ResLabel names a resolution by width so scope films (1920x800) read as 1080p.
func ResLabel(w, h int) string {
	switch {
	case w >= 3200 || h >= 2000:
		return "4K"
	case w >= 1800 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	case w >= 900 || h >= 540:
		return "576p"
	}
	return fmt.Sprintf("%dp", h)
}

func describeAudio(a *store.Stream, copy bool, br int) string {
	s := strings.ToUpper(a.Codec) + " " + chStr(a.Channels)
	if copy {
		s += " (copy)"
	}
	return s
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Duration returns the file's duration (0 if unknown).
func (p Params) Duration(f *store.File) float64 {
	if f.Info != nil {
		return f.Info.Duration
	}
	return f.Duration
}
