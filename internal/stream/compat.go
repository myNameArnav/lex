package stream

import (
	"path/filepath"
	"strings"

	"lex/internal/store"
)

// Compatibility tiers, from no work at all to not playable. They answer
// "how well does this file play in this browser?" without codec names.
const (
	TierDirect     = "direct"     // the browser plays the file as it is
	TierBrowser    = "browser"    // the browser repackages it itself; no server work
	TierRemux      = "remux"      // the server repackages it; video untouched
	TierTranscode  = "transcode"  // the server re-encodes the video
	TierUnplayable = "unplayable" // no plan is possible
	TierUnknown    = "unknown"    // not analysed yet, or no video
)

// Cause explains part of a verdict. Stage "direct" says why the file can't
// play as it is; "stream" says why the server does more than repackage.
type Cause struct {
	Stage string `json:"stage"` // direct | stream
	Kind  string `json:"kind"`  // container | video | audio | hls | bitrate | mode | setting | error
	Key   string `json:"key"`   // capability key or codec (mkv, hevc10, dts…)
}

type Verdict struct {
	Tier   string  `json:"tier"`
	Causes []Cause `json:"causes"`
}

// LikelyHLSCopy guesses HLS.CanCopy without reading the file: Matroska
// files normally carry the keyframe index it needs. Used for library-wide
// reports, where opening every file would be slow on a spinning disk.
func LikelyHLSCopy(f *store.File) bool {
	switch strings.ToLower(filepath.Ext(f.Path)) {
	case ".mkv", ".mk3d", ".webm":
		return true
	}
	return false
}

// Assess runs Decide for req (whose Audio and Subtitle should be -1 for the
// file's defaults) and explains the result. The tier always comes from
// Decide; the causes mirror its checks so the answer reads in plain terms.
func Assess(cfg store.Config, f *store.File, req PlanRequest, remote bool) Verdict {
	if f.Info == nil || f.Info.Video() == nil {
		return Verdict{Tier: TierUnknown, Causes: []Cause{}}
	}
	// "Prefer Direct Play" makes Decide direct-play even what the browser
	// can't decode; the question here is what would actually work.
	if req.Mode == "direct" {
		req.Mode = "auto"
	}
	plan, err := Decide(cfg, f, req, remote)
	if err != nil {
		return Verdict{Tier: TierUnplayable, Causes: []Cause{{Stage: "stream", Kind: "error", Key: err.Error()}}}
	}
	v := Verdict{Causes: []Cause{}}
	switch {
	case plan.Method == "direct":
		v.Tier = TierDirect
		return v
	case plan.ClientRemux:
		v.Tier = TierBrowser
	case plan.VideoCopy:
		v.Tier = TierRemux
	default:
		v.Tier = TierTranscode
	}

	add := func(stage, kind, key string) { v.Causes = append(v.Causes, Cause{stage, kind, key}) }
	vs := f.Info.Video()
	vkey, ckey := VideoKey(vs), ContainerKey(f.Path)
	a := f.Info.StreamByIndex(plan.Audio)
	caps := req.Caps
	limit := plan.LimitKbps
	overLimit := limit > 0 && int(f.Info.Bitrate/1000) > limit

	// Why not as it is (Decide's direct-play checks, in order).
	switch {
	case req.Mode == "remux" || req.Mode == "transcode":
		add("direct", "mode", req.Mode)
	case !cfg.EnableDirectPlay:
		add("direct", "setting", "directplay")
	case !caps.Direct[ckey]:
		add("direct", "container", ckey)
	case !caps.NativeVideo[vkey]:
		add("direct", "video", vkey)
	case a != nil && !caps.NativeAudio[a.Codec]:
		add("direct", "audio", a.Codec)
	case a != nil && defaultAudio(f.Info) != nil && a.Index != defaultAudio(f.Info).Index:
		add("direct", "track", a.Codec)
	case overLimit:
		add("direct", "bitrate", "")
	}
	if v.Tier == TierBrowser {
		return v
	}

	// Why the server does more than repackage.
	if !plan.VideoCopy {
		videoCaps := caps.Video
		if plan.HLS {
			videoCaps = caps.NativeVideo
		}
		switch {
		case req.Mode == "transcode":
			add("stream", "mode", "transcode")
		case !cfg.EnableRemux:
			add("stream", "setting", "remux")
		case !videoCaps[vkey]:
			add("stream", "video", vkey)
		case overLimit:
			add("stream", "bitrate", "")
		case plan.HLS && !req.HLSCopy:
			add("stream", "hls", ckey)
		}
	}
	if a != nil && !plan.AudioCopy {
		add("stream", "audio", a.Codec)
	}
	return v
}
