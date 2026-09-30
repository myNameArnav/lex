package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lex/internal/proc"
	"lex/internal/store"
)

type ffStream struct {
	Index         int               `json:"index"`
	CodecType     string            `json:"codec_type"`
	CodecName     string            `json:"codec_name"`
	CodecTag      string            `json:"codec_tag_string"`
	Profile       string            `json:"profile"`
	Level         int               `json:"level"`
	PixFmt        string            `json:"pix_fmt"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	RFrameRate    string            `json:"r_frame_rate"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	ColorTransfer string            `json:"color_transfer"`
	BitsPerRaw    string            `json:"bits_per_raw_sample"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	SampleRate    string            `json:"sample_rate"`
	BitRate       string            `json:"bit_rate"`
	Tags          map[string]string `json:"tags"`
	Disposition   map[string]int    `json:"disposition"`
	SideData      []map[string]any  `json:"side_data_list"`
}

type ffProbe struct {
	Format struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		StartTime  string            `json:"start_time"`
		BitRate    string            `json:"bit_rate"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
	Streams  []ffStream `json:"streams"`
	Chapters []struct {
		StartTime string            `json:"start_time"`
		EndTime   string            `json:"end_time"`
		Tags      map[string]string `json:"tags"`
	} `json:"chapters"`
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func parseRate(r string) float64 {
	a, b, ok := strings.Cut(r, "/")
	if !ok {
		return atof(r)
	}
	d := atof(b)
	if d == 0 {
		return 0
	}
	return atof(a) / d
}

// Probe runs ffprobe on a file and summarises the result.
func Probe(ctx context.Context, ffprobe, path string) (*store.MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json", "-show_format", "-show_streams", "-show_chapters", "-i", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	// Probing is background work: never compete with playback.
	proc.Nice(cmd.Process.Pid, 10)
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffprobe: %s", msg)
	}
	out := stdout.Bytes()
	var p ffProbe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	info := &store.MediaInfo{
		Format:    p.Format.FormatName,
		Duration:  atof(p.Format.Duration),
		StartTime: atof(p.Format.StartTime),
		Bitrate:   atoi64(p.Format.BitRate),
	}
	for _, c := range p.Chapters {
		info.Chapters = append(info.Chapters, store.Chapter{Start: atof(c.StartTime), End: atof(c.EndTime), Title: c.Tags["title"]})
	}
	for _, fs := range p.Streams {
		s := store.Stream{
			Index:    fs.Index,
			Type:     fs.CodecType,
			Codec:    fs.CodecName,
			Profile:  fs.Profile,
			Level:    fs.Level,
			Language: normLang(tag(fs.Tags, "language")),
			Title:    tag(fs.Tags, "title"),
			Default:  fs.Disposition["default"] == 1,
			Forced:   fs.Disposition["forced"] == 1,
			Bitrate:  atoi64(fs.BitRate),
		}
		if s.Bitrate == 0 {
			s.Bitrate = atoi64(tag(fs.Tags, "BPS"))
		}
		switch fs.CodecType {
		case "video":
			// Cover art is exposed as a video stream; skip it.
			if fs.Disposition["attached_pic"] == 1 || fs.CodecName == "mjpeg" || fs.CodecName == "png" {
				continue
			}
			s.Width, s.Height, s.PixFmt = fs.Width, fs.Height, fs.PixFmt
			s.FrameRate = parseRate(fs.AvgFrameRate)
			if s.FrameRate == 0 || s.FrameRate > 240 {
				s.FrameRate = parseRate(fs.RFrameRate)
			}
			s.BitDepth = 8
			if strings.Contains(fs.PixFmt, "10") || fs.BitsPerRaw == "10" {
				s.BitDepth = 10
			} else if strings.Contains(fs.PixFmt, "12") {
				s.BitDepth = 12
			}
			s.HDR, s.DVProfile, s.DVCompat = hdrInfo(fs)
			s.CodecString = videoCodecString(&s)
		case "audio":
			s.Channels, s.ChannelLayout = fs.Channels, fs.ChannelLayout
			s.SampleRate = int(atoi64(fs.SampleRate))
			s.CodecString = audioCodecString(&s)
		case "subtitle":
			s.TextSub = IsTextSubCodec(fs.CodecName)
		default:
			continue
		}
		info.Streams = append(info.Streams, s)
	}
	if info.Bitrate == 0 && info.Duration > 0 {
		if st, err := os.Stat(path); err == nil {
			info.Bitrate = int64(float64(st.Size()*8) / info.Duration)
		}
	}
	return info, nil
}

func tag(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	for kk, v := range m {
		if strings.EqualFold(kk, k) || strings.HasPrefix(strings.ToUpper(kk), strings.ToUpper(k)+"-") {
			return v
		}
	}
	return ""
}

func IsTextSubCodec(c string) bool {
	switch c {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text", "subviewer", "subviewer1", "microdvd", "sami", "realtext", "jacosub", "mpl2", "vplayer", "pjs", "stl":
		return true
	}
	return false
}

func hdrInfo(fs ffStream) (string, int, int) {
	var parts []string
	dvProfile, dvCompat := 0, 0
	hdr10plus := false
	for _, sd := range fs.SideData {
		t, _ := sd["side_data_type"].(string)
		switch {
		case strings.Contains(t, "DOVI"):
			if v, ok := sd["dv_profile"].(float64); ok {
				dvProfile = int(v)
			}
			if v, ok := sd["dv_bl_signal_compatibility_id"].(float64); ok {
				dvCompat = int(v)
			}
		case strings.Contains(t, "2094-40"):
			hdr10plus = true
		}
	}
	if dvProfile > 0 || fs.CodecTag == "dvh1" || fs.CodecTag == "dvhe" {
		parts = append(parts, "DV")
	}
	switch fs.ColorTransfer {
	case "smpte2084":
		if hdr10plus {
			parts = append(parts, "HDR10+")
		} else {
			parts = append(parts, "HDR10")
		}
	case "arib-std-b67":
		parts = append(parts, "HLG")
	}
	return strings.Join(parts, "+"), dvProfile, dvCompat
}

func videoCodecString(s *store.Stream) string {
	switch s.Codec {
	case "h264":
		prof, cons := 0x64, 0x00
		switch strings.ToLower(s.Profile) {
		case "constrained baseline":
			prof, cons = 0x42, 0xE0
		case "baseline":
			prof, cons = 0x42, 0x00
		case "main":
			prof, cons = 0x4D, 0x40
		case "extended":
			prof = 0x58
		case "high":
			prof = 0x64
		case "high 10", "high 10 intra":
			prof = 0x6E
		case "high 4:2:2", "high 4:2:2 intra":
			prof = 0x7A
		case "high 4:4:4 predictive", "high 4:4:4 intra":
			prof = 0xF4
		}
		lvl := s.Level
		if lvl <= 0 {
			lvl = 40
		}
		return fmt.Sprintf("avc1.%02X%02X%02X", prof, cons, lvl)
	case "hevc":
		lvl := s.Level
		if lvl <= 0 {
			lvl = 120
		}
		p := strings.ToLower(s.Profile)
		switch {
		case strings.Contains(p, "main 10") || s.BitDepth == 10:
			return fmt.Sprintf("hvc1.2.4.L%d.B0", lvl)
		case strings.Contains(p, "rext") || s.BitDepth > 10:
			return fmt.Sprintf("hvc1.4.10.L%d.B0", lvl)
		default:
			return fmt.Sprintf("hvc1.1.6.L%d.B0", lvl)
		}
	case "av1":
		prof := 0
		switch strings.ToLower(s.Profile) {
		case "high":
			prof = 1
		case "professional":
			prof = 2
		}
		lvl := s.Level
		if lvl < 0 || lvl > 31 {
			lvl = 8
		}
		return fmt.Sprintf("av01.%d.%02dM.%02d", prof, lvl, max(s.BitDepth, 8))
	case "vp9":
		prof := 0
		if strings.Contains(s.Profile, "2") || s.BitDepth > 8 {
			prof = 2
		}
		return fmt.Sprintf("vp09.%02d.40.%02d", prof, max(s.BitDepth, 8))
	case "vp8":
		return "vp8"
	}
	return ""
}

func audioCodecString(s *store.Stream) string {
	switch s.Codec {
	case "aac":
		switch strings.ToLower(s.Profile) {
		case "he-aac":
			return "mp4a.40.5"
		case "he-aacv2":
			return "mp4a.40.29"
		}
		return "mp4a.40.2"
	case "mp3":
		return "mp4a.40.34"
	case "ac3":
		return "ac-3"
	case "eac3":
		return "ec-3"
	case "opus":
		return "opus"
	case "flac":
		return "fLaC"
	case "alac":
		return "alac"
	case "vorbis":
		return "vorbis"
	}
	return ""
}

var langNames = map[string]string{
	"en": "eng", "english": "eng", "ja": "jpn", "jp": "jpn", "japanese": "jpn", "es": "spa", "spanish": "spa", "fr": "fre", "fra": "fre", "french": "fre",
	"de": "ger", "deu": "ger", "german": "ger", "it": "ita", "italian": "ita", "hi": "hin", "hindi": "hin", "ru": "rus", "russian": "rus",
	"pt": "por", "portuguese": "por", "zh": "chi", "zho": "chi", "chinese": "chi", "ko": "kor", "korean": "kor", "ar": "ara", "arabic": "ara",
	"nl": "dut", "nld": "dut", "dutch": "dut", "sv": "swe", "swedish": "swe", "pl": "pol", "polish": "pol", "tr": "tur", "turkish": "tur",
	"ta": "tam", "tamil": "tam", "te": "tel", "telugu": "tel", "und": "",
}

func normLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	if v, ok := langNames[l]; ok {
		return v
	}
	return l
}

// ExternalSubs finds sidecar subtitle files for a video. Indexes start at
// 1000 so they never collide with embedded stream indexes.
func ExternalSubs(videoPath string) []store.Stream {
	dir := filepath.Dir(videoPath)
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	var cands []string
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			if !e.IsDir() && IsSubtitle(n) && strings.HasPrefix(n, base) {
				cands = append(cands, filepath.Join(dir, n))
			}
		}
		// Subs/ folders (common in scene/YTS releases) only when the folder
		// holds a single video, otherwise we can't tell which video they belong to.
		for _, e := range ents {
			if e.IsDir() && strings.EqualFold(e.Name(), "subs") && countVideos(ents) == 1 {
				sub := filepath.Join(dir, e.Name())
				if se, err := os.ReadDir(sub); err == nil {
					for _, f := range se {
						if !f.IsDir() && IsSubtitle(f.Name()) {
							cands = append(cands, filepath.Join(sub, f.Name()))
						}
					}
				}
			}
		}
	}
	sort.Strings(cands)
	var out []store.Stream
	for i, p := range cands {
		n := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		n = strings.TrimPrefix(n, base)
		tokens := strings.FieldsFunc(n, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' })
		s := store.Stream{Index: 1000 + i, Type: "subtitle", Codec: strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), "."), TextSub: true, External: true, ExternalPath: p}
		if s.Codec == "srt" {
			s.Codec = "subrip"
		}
		for _, t := range tokens {
			lt := strings.ToLower(t)
			switch {
			case lt == "forced":
				s.Forced = true
			case lt == "sdh" || lt == "cc" || lt == "hi":
				s.Title = strings.TrimSpace(s.Title + " SDH")
			case lt == "default":
				s.Default = true
			case s.Language == "" && (len(lt) == 2 || len(lt) == 3 || langNames[lt] != ""):
				if _, err := strconv.Atoi(lt); err == nil {
					continue
				}
				s.Language = normLang(lt)
			default:
				if s.Title == "" {
					s.Title = t
				}
			}
		}
		out = append(out, s)
	}
	return out
}

func countVideos(ents []os.DirEntry) int {
	n := 0
	for _, e := range ents {
		if !e.IsDir() && IsVideo(e.Name()) {
			n++
		}
	}
	return n
}
