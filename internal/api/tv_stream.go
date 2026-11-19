package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lex/internal/proc"
	"lex/internal/store"
	"lex/internal/tv"
)

// Live channels from the internet (IPTV). The server fetches the stream,
// with the channel's User-Agent and Referer, which many providers require
// and a browser can't send, and hands the browser fragmented MP4: the video
// as it is when it's H.264, else encoded to H.264, and the audio as AAC.

// streamProtocols are all ffmpeg may open for a stream: no local files.
const streamProtocols = "http,https,tls,tcp,crypto,hls,httpproxy"

// liveItem stands in for a library item in a live channel's program, so the
// guide and the player show it like any other.
func liveItem(title, sub, desc string) *store.Item {
	it := &store.Item{Kind: "live", Title: title, Overview: desc}
	if sub != "" {
		it.OriginalTitle = sub
	}
	return it
}

// streamPrograms are a live channel's programs in [from, to): its guide's,
// with what the guide leaves out (all of it, without one) as blocks named
// after the channel, so there's always something on to tune into. limit
// <= 0: all of them.
func (s *Server) streamPrograms(ch tv.Channel, from, to time.Time, limit int) []programView {
	out := []programView{}
	add := func(p programView) bool {
		if limit > 0 && len(out) >= limit {
			return false
		}
		out = append(out, p)
		return true
	}
	filler := func(a, b time.Time) programView {
		return programView{Item: liveItem(ch.Name, "", ""), Start: a.UnixMilli(), End: b.UnixMilli()}
	}
	at := from
	for _, p := range s.TV.StreamPrograms(ch, from, to) {
		if p.Start.Sub(at) >= time.Minute && !add(filler(at, p.Start)) {
			return out
		}
		if p.End.After(at) {
			if !add(programView{Item: liveItem(p.Title, p.Subtitle, p.Description), Start: p.Start.UnixMilli(), End: p.End.UnixMilli()}) {
				return out
			}
			at = p.End
		}
	}
	if to.Sub(at) >= time.Minute {
		add(filler(at, to))
	}
	return out
}

// tuneStream answers a tune to a live channel: where to fetch it and its
// guide's programs now and next (none without a guide).
func (s *Server) tuneStream(w http.ResponseWriter, ch tv.Channel, now time.Time) {
	out := map[string]any{"channel": ch, "serverTime": now.UnixMilli(), "stream": map[string]any{
		"url":  "/api/tv/" + ch.ID + "/stream",
		"logo": ch.Stream.Logo,
	}}
	// Now is the guide's program airing now (none in a gap of the guide);
	// next is the one after.
	pv := func(p tv.GuideProgram) programView {
		return programView{Item: liveItem(p.Title, p.Subtitle, p.Description), Start: p.Start.UnixMilli(), End: p.End.UnixMilli()}
	}
	for _, p := range s.TV.StreamPrograms(ch, now, now.Add(12*time.Hour)) {
		if !p.Start.After(now) && out["now"] == nil {
			out["now"] = pv(p)
		} else if p.Start.After(now) {
			out["next"] = pv(p)
			break
		}
	}
	writeJSON(w, out)
}

// streamProbe is what ffprobe found in a stream: the variant to play (an
// HLS program id, -1 for none) and its codecs.
type streamProbe struct {
	Program int    `json:"-"`
	Video   string `json:"video"`
	Audio   string `json:"audio"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	// For the stats panel.
	VideoProfile string  `json:"-"`
	AudioProfile string  `json:"-"`
	FPS          float64 `json:"-"`
	Interlaced   bool    `json:"-"`
	SampleRate   int     `json:"-"`
	Channels     int     `json:"-"`
	Bitrate      int     `json:"-"` // the HLS variant's, bit/s; 0 unknown
	at           time.Time
}

var (
	probeMu    sync.Mutex
	probeCache = map[string]streamProbe{}
)

// inputArgs are ffmpeg's (and ffprobe's) options for reading st.
func inputArgs(st store.TVStream) []string {
	args := []string{"-protocol_whitelist", streamProtocols}
	if st.UserAgent != "" {
		args = append(args, "-user_agent", st.UserAgent)
	}
	var hs strings.Builder
	if st.Referrer != "" {
		hs.WriteString("Referer: " + st.Referrer + "\r\n")
	}
	for _, hd := range st.Headers {
		hs.WriteString(hd.Name + ": " + hd.Value + "\r\n")
	}
	if hs.Len() > 0 {
		args = append(args, "-headers", hs.String())
	}
	return append(args, "-i", st.URL)
}

// probeStream reads st's variants and codecs; kept for ten minutes.
func (s *Server) probeStream(ctx context.Context, st store.TVStream) (streamProbe, error) {
	key := fmt.Sprint(st.URL, st.UserAgent, st.Referrer, st.Headers)
	probeMu.Lock()
	p, ok := probeCache[key]
	probeMu.Unlock()
	if ok && time.Since(p.at) < 10*time.Minute {
		return p, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := append([]string{"-v", "error", "-show_entries", "program=program_id:program_tags=variant_bitrate:stream=index,codec_type,codec_name,profile,width,height,r_frame_rate,field_order,sample_rate,channels", "-of", "json"}, inputArgs(st)...)
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, s.FF.Probe, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(lastLine(stderr.String()))
		if ctx.Err() != nil {
			msg = "the stream didn't answer in time"
		}
		if msg == "" {
			msg = err.Error()
		}
		return streamProbe{}, errors.New(msg)
	}
	type pstream struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Profile    string `json:"profile"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		FrameRate  string `json:"r_frame_rate"`
		FieldOrder string `json:"field_order"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
	}
	var r struct {
		Programs []struct {
			ID   int `json:"program_id"`
			Tags struct {
				Bitrate string `json:"variant_bitrate"`
			} `json:"tags"`
			Streams []pstream `json:"streams"`
		} `json:"programs"`
		Streams []pstream `json:"streams"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return streamProbe{}, err
	}
	p = streamProbe{Program: -1}
	streams := r.Streams
	// An HLS master playlist: the best variant within the transcode cap
	// (the one with the highest bitrate), else the smallest.
	maxH := s.St.Config().MaxTranscodeHeight
	best, smallest := -1, -1
	rate := func(i int) int { b, _ := strconv.Atoi(r.Programs[i].Tags.Bitrate); return b }
	for i, pr := range r.Programs {
		h := 0
		for _, ps := range pr.Streams {
			if ps.CodecType == "video" {
				h = ps.Height
			}
		}
		if smallest < 0 || rate(i) < rate(smallest) {
			smallest = i
		}
		if (maxH <= 0 || h <= maxH) && (best < 0 || rate(i) > rate(best)) {
			best = i
		}
	}
	if best < 0 {
		best = smallest
	}
	if len(r.Programs) > 1 && best >= 0 {
		p.Program, streams = r.Programs[best].ID, r.Programs[best].Streams
	}
	if best >= 0 {
		p.Bitrate = rate(best)
	}
	for _, ps := range streams {
		switch {
		case ps.CodecType == "video" && p.Video == "":
			p.Video, p.Width, p.Height, p.VideoProfile = ps.CodecName, ps.Width, ps.Height, ps.Profile
			p.Interlaced = ps.FieldOrder != "" && ps.FieldOrder != "progressive" && ps.FieldOrder != "unknown"
			if a, b, ok := strings.Cut(ps.FrameRate, "/"); ok {
				n, _ := strconv.ParseFloat(a, 64)
				d, _ := strconv.ParseFloat(b, 64)
				if d > 0 {
					p.FPS = n / d
				}
			}
		case ps.CodecType == "audio" && p.Audio == "":
			p.Audio, p.AudioProfile, p.Channels = ps.CodecName, ps.Profile, ps.Channels
			p.SampleRate, _ = strconv.Atoi(ps.SampleRate)
		}
	}
	if p.Video == "" && p.Audio == "" {
		return streamProbe{}, errors.New("no video or audio found")
	}
	p.at = time.Now()
	probeMu.Lock()
	for k, v := range probeCache {
		if time.Since(v.at) >= 10*time.Minute {
			delete(probeCache, k)
		}
	}
	probeCache[key] = p
	probeMu.Unlock()
	return p, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// liveStreams counts the streams being fetched, at most maxLiveStreams.
var liveStreams = struct {
	sync.Mutex
	n int
}{}

const maxLiveStreams = 8

// tvStream sends a live channel to the browser as fragmented MP4, for as
// long as it's watched. Video needing an encode takes a transcode slot.
func (s *Server) tvStream(w http.ResponseWriter, r *http.Request) {
	ch, ok, err := s.TV.Channel(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok || ch.Stream == nil {
		writeErr(w, 404, "no such live channel")
		return
	}
	st := *ch.Stream
	p, err := s.probeStream(r.Context(), st)
	if err != nil {
		writeErr(w, 502, "Couldn’t open the stream: "+err.Error())
		return
	}
	cfg := s.St.Config()
	copyVideo := p.Video == "" || p.Video == "h264"
	slot := ""
	if !copyVideo {
		slot = "live-" + store.RandomToken(6)
		if s.Sess != nil && s.Sess.Slots != nil {
			if !s.Sess.Slots.Acquire(slot, cfg.MaxTranscodes) {
				writeErr(w, 503, "The server is already converting as many videos as it’s allowed to")
				return
			}
			defer s.Sess.Slots.Release(slot)
		}
	}
	liveStreams.Lock()
	if liveStreams.n >= maxLiveStreams {
		liveStreams.Unlock()
		writeErr(w, 503, "Too many live channels are playing")
		return
	}
	liveStreams.n++
	liveStreams.Unlock()
	defer func() { liveStreams.Lock(); liveStreams.n--; liveStreams.Unlock() }()

	args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, inputArgs(st)...)
	sel := "0:"
	if p.Program >= 0 {
		sel = fmt.Sprintf("0:p:%d:", p.Program)
	}
	if p.Video != "" {
		args = append(args, "-map", sel+"v:0")
	}
	if p.Audio != "" {
		args = append(args, "-map", sel+"a:0")
	}
	args = append(args, "-sn", "-dn")
	if copyVideo {
		// Joining a live stream starts at a keyframe, but with open GOPs
		// the B-frames after it may show before it and lean on the GOP
		// before, which was never sent. Chrome skips them; macOS's decoder
		// (Firefox, Safari) fails the whole stream. Drop them: only at the
		// start, so a provider whose timestamps jump back later loses nothing.
		args = append(args, "-c:v", "copy", "-bsf:v", "noise=drop='lt(pts,startpts)*lt(n,30)'")
	} else {
		h := p.Height
		if cfg.MaxTranscodeHeight > 0 && (h <= 0 || h > cfg.MaxTranscodeHeight) {
			h = cfg.MaxTranscodeHeight
		}
		if h > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=-2:'min(%d,ih)',format=yuv420p", h))
		}
		if cfg.VideoEncoder == "h264_v4l2m2m" && s.FF.V4L2Device {
			args = append(args, "-c:v", "h264_v4l2m2m", "-b:v", "4000k", "-g", "50", "-bf", "0", "-num_capture_buffers", "32")
		} else {
			args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-maxrate", "5000k", "-bufsize", "10000k", "-g", "50", "-sc_threshold", "0")
		}
	}
	if p.Audio != "" {
		if p.Audio == "aac" {
			// MPEG-TS carries AAC with ADTS headers, which MP4 can't hold.
			args = append(args, "-c:a", "copy", "-bsf:a", "aac_adtstoasc")
		} else {
			args = append(args, "-c:a", "aac", "-ac", "2", "-b:a", "160k")
		}
	}
	// delay_moov: the header waits for the first packets, so it carries
	// the AAC config that aac_adtstoasc only learns from them. Without it
	// the header has none: Chrome guesses, Firefox plays no sound.
	args = append(args, "-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof+delay_moov", "-frag_duration", "2000000", "pipe:1")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.FF.Path, args...)
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := cmd.Start(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cfg.FFmpegNice > 0 {
		proc.Nice(cmd.Process.Pid, cfg.FFmpegNice)
	}
	method := "video copied"
	if !copyVideo {
		method = "video converted from " + p.Video
	}
	s.Log.Infof("Live TV: %s watching %s (%s)", userOf(r).Name, st.Name, method)
	start := time.Now()
	ls := &liveSend{user: userOf(r).ID, channel: ch.ID, start: start, probe: p, convert: !copyVideo, stderr: &stderr}
	key := liveKey(ls.user, ch.ID, r.URL.Query().Get("t"))
	liveSends.Lock()
	liveSends.m[key] = ls
	liveSends.Unlock()
	defer func() {
		liveSends.Lock()
		if liveSends.m[key] == ls {
			delete(liveSends.m, key)
		}
		liveSends.Unlock()
		s.Log.Infof("Live TV: %s stopped %s after %s", userOf(r).Name, st.Name, time.Since(start).Round(time.Second))
	}()

	// Nothing is sent until ffmpeg has output: a stream that fails to open
	// answers with an error the player can show.
	buf := make([]byte, 64<<10)
	n, rerr := io.ReadAtLeast(out, buf, 1)
	if n == 0 {
		cancel()
		_ = cmd.Wait()
		msg := strings.TrimSpace(lastLine(stderr.String()))
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		writeErr(w, 502, "The stream stopped: "+msg)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	for {
		if _, err := w.Write(buf[:n]); err != nil {
			break
		}
		ls.bytes.Add(int64(n))
		if flusher != nil {
			flusher.Flush()
		}
		n, err = out.Read(buf)
		if err != nil {
			break
		}
	}
	stopped := r.Context().Err() != nil
	cancel()
	_ = cmd.Wait()
	if msg := strings.TrimSpace(lastLine(stderr.String())); msg != "" && !stopped {
		s.Log.Warnf("Live TV: %s ended: %s", st.Name, msg)
	}
}

// liveSend is a live channel being sent to a player, for its stats.
type liveSend struct {
	user    int64
	channel string
	start   time.Time
	probe   streamProbe
	convert bool
	bytes   atomic.Int64
	stderr  *lockedBuffer
}

// liveSends are the streams being sent, by liveKey: the player's request
// names its stream with ?t=, which the stats ask about.
var liveSends = struct {
	sync.Mutex
	m map[string]*liveSend
}{m: map[string]*liveSend{}}

func liveKey(user int64, channel, t string) string { return fmt.Sprint(user, "/", channel, "/", t) }

// lockedBuffer is ffmpeg's stderr, read while ffmpeg writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Only the last of it matters; a stream that warns all day can't grow it.
	if l.b.Len() > 64<<10 {
		rest := append([]byte(nil), l.b.Bytes()[l.b.Len()-8<<10:]...)
		l.b.Reset()
		l.b.Write(rest)
	}
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// tvStreamInfo is the stats panel's view of a live channel playing here
// (?t= names the player's request): what the stream holds, what the server
// does with it, and how much it has sent. Without a stream being sent, the
// probe alone.
func (s *Server) tvStreamInfo(w http.ResponseWriter, r *http.Request) {
	ch, ok, err := s.TV.Channel(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok || ch.Stream == nil {
		writeErr(w, 404, "no such live channel")
		return
	}
	out := map[string]any{"host": streamHost(ch.Stream.URL), "playing": false}
	liveSends.Lock()
	ls := liveSends.m[liveKey(userOf(r).ID, ch.ID, r.URL.Query().Get("t"))]
	liveSends.Unlock()
	var p streamProbe
	if ls != nil {
		p = ls.probe
		out["playing"] = true
		out["bytes"] = ls.bytes.Load()
		out["seconds"] = time.Since(ls.start).Seconds()
		out["convert"] = ls.convert
		if msg := strings.TrimSpace(lastLine(ls.stderr.String())); msg != "" {
			out["warning"] = msg
		}
	} else {
		if p, err = s.probeStream(r.Context(), *ch.Stream); err != nil {
			writeErr(w, 502, "Couldn’t open the stream: "+err.Error())
			return
		}
		out["convert"] = p.Video != "" && p.Video != "h264"
	}
	out["video"], out["audio"], out["width"], out["height"] = p.Video, p.Audio, p.Width, p.Height
	out["videoProfile"], out["audioProfile"], out["fps"], out["interlaced"] = p.VideoProfile, p.AudioProfile, p.FPS, p.Interlaced
	out["sampleRate"], out["channels"], out["bitrate"] = p.SampleRate, p.Channels, p.Bitrate
	writeJSON(w, out)
}

// streamHost is the host a stream comes from, without its path (which
// may hold a token).
func streamHost(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return pu.Host
	}
	return ""
}

// tvStreamProbe checks a stream before it's saved: whether it opens, and
// what it holds.
func (s *Server) tvStreamProbe(w http.ResponseWriter, r *http.Request) {
	var st store.TVStream
	if err := readJSON(r, &st); err != nil {
		writeErr(w, 400, "bad stream: "+err.Error())
		return
	}
	st = st.Clean()
	if !store.WebURL(st.URL) {
		writeErr(w, 400, "Enter the stream’s http(s) address")
		return
	}
	p, err := s.probeStream(r.Context(), st)
	if err != nil {
		writeErr(w, 502, "Couldn’t open the stream: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"video": p.Video, "audio": p.Audio, "width": p.Width, "height": p.Height,
		"convert": p.Video != "" && p.Video != "h264"})
}

// ---------- Settings › Live TV: streams and sources ----------

func checkStream(st store.TVStream) error {
	if st.Name == "" {
		return errBadChannel("Give the channel a name")
	}
	if !store.WebURL(st.URL) {
		return errBadChannel("Enter the stream’s http(s) address")
	}
	return nil
}

// maxStreamsAdd is how many live channels can be added at once.
const maxStreamsAdd = 2000

// tvStreamsAdd adds live channels at the end of the lineup: one (the
// stream dialog) or several (picked from a playlist).
func (s *Server) tvStreamsAdd(w http.ResponseWriter, r *http.Request) {
	var in []store.TVStream
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad streams: "+err.Error())
		return
	}
	if len(in) == 0 || len(in) > maxStreamsAdd {
		writeErr(w, 400, fmt.Sprintf("Add between 1 and %d channels at a time", maxStreamsAdd))
		return
	}
	for i := range in {
		in[i] = in[i].Clean()
		if err := checkStream(in[i]); err != nil {
			s.writeChannelErr(w, err)
			return
		}
	}
	chs, err := s.TV.Lineup()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var saved string
	_, err = s.St.UpdateConfig(func(c *store.Config) error {
		order := []string{}
		for _, o := range chs {
			order = append(order, o.ID)
		}
		for _, st := range in {
			st.ID = store.RandomToken(4)
			for slices.ContainsFunc(c.TVStreams, func(o store.TVStream) bool { return o.ID == st.ID }) {
				st.ID = store.RandomToken(4)
			}
			if saved == "" {
				saved = "stream-" + st.ID
			}
			c.TVStreams = append(c.TVStreams, st)
			order = append(order, "stream-"+st.ID)
		}
		c.TVOrder = order
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.Log.Infof("Live TV: %d live channel(s) added by %s", len(in), userOf(r).Name)
	s.TV.RefreshGuides()
	s.writeLineup(w, saved)
}

func (s *Server) tvStreamUpdate(w http.ResponseWriter, r *http.Request) {
	var st store.TVStream
	if err := readJSON(r, &st); err != nil {
		writeErr(w, 400, "bad stream: "+err.Error())
		return
	}
	st = st.Clean()
	st.ID = r.PathValue("id")
	if err := checkStream(st); err != nil {
		s.writeChannelErr(w, err)
		return
	}
	_, err := s.St.UpdateConfig(func(c *store.Config) error {
		i := slices.IndexFunc(c.TVStreams, func(o store.TVStream) bool { return o.ID == st.ID })
		if i < 0 {
			return store.ErrNotFound
		}
		st.Source = c.TVStreams[i].Source
		c.TVStreams[i] = st
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.writeLineup(w, "")
}

func (s *Server) tvStreamDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := s.St.UpdateConfig(func(c *store.Config) error {
		i := slices.IndexFunc(c.TVStreams, func(o store.TVStream) bool { return o.ID == id })
		if i < 0 {
			return store.ErrNotFound
		}
		c.TVStreams = slices.Delete(c.TVStreams, i, i+1)
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.writeLineup(w, "")
}

// tvSourcesSave replaces the playlists. Channels picked from a playlist
// that's removed stay (they no longer refresh their guide from it).
func (s *Server) tvSourcesSave(w http.ResponseWriter, r *http.Request) {
	var in []store.TVSource
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad sources: "+err.Error())
		return
	}
	old := s.St.Config().TVSources
	for i := range in {
		in[i] = in[i].Clean()
		if !store.WebURL(in[i].URL) && in[i].PlaylistFile == "" {
			writeErr(w, 400, "Enter the playlist’s http(s) address, or upload the file")
			return
		}
		if in[i].GuideURL != "" && !store.WebURL(in[i].GuideURL) {
			writeErr(w, 400, "Enter the guide’s http(s) address, or leave it empty")
			return
		}
		// A new or changed playlist must list channels: say now if it's a
		// single stream, or not a playlist at all.
		if !slices.ContainsFunc(old, func(o store.TVSource) bool {
			return o.URL == in[i].URL && o.PlaylistFile == in[i].PlaylistFile && o.UserAgent == in[i].UserAgent && fmt.Sprint(o.Headers) == fmt.Sprint(in[i].Headers)
		}) {
			if _, err := s.TV.Playlist(r.Context(), in[i]); err != nil {
				writeErr(w, 400, "Couldn’t use the playlist: "+err.Error())
				return
			}
		}
	}
	_, err := s.St.UpdateConfig(func(c *store.Config) error { c.TVSources = in; return nil })
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.removeUnusedUploads()
	s.TV.RefreshGuides()
	s.writeLineup(w, "")
}

// Uploaded playlists and guides: at most this big (a guide may be gzipped).
const (
	maxPlaylistUpload = 64 << 20
	maxGuideUpload    = 256 << 20
)

// tvUpload keeps an uploaded playlist (?kind=playlist) or guide
// (?kind=guide) for a playlist to use, after checking it is one; it answers
// the file's name for the playlist, and how many channels a playlist has.
// Files no playlist uses are removed when the playlists are next saved.
func (s *Server) tvUpload(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	limit, ext := int64(maxPlaylistUpload), "m3u"
	switch kind {
	case "playlist":
	case "guide":
		limit, ext = maxGuideUpload, "xmltv"
	default:
		writeErr(w, 400, "kind must be playlist or guide")
		return
	}
	dir := s.TV.UploadDir()
	if dir == "" {
		writeErr(w, 500, "no data folder")
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	name := store.RandomToken(8) + "." + ext
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	fail := func(code int, msg string) { os.Remove(path); writeErr(w, code, msg) }
	switch {
	case err != nil:
		fail(400, "upload failed: "+err.Error())
		return
	case n > limit:
		fail(413, fmt.Sprintf("The file is too big (at most %d MB)", limit>>20))
		return
	case n == 0:
		fail(400, "The file is empty")
		return
	}
	out := map[string]any{"file": name}
	if kind == "playlist" {
		entries, err := s.TV.Playlist(r.Context(), store.TVSource{PlaylistFile: name})
		if err != nil {
			fail(400, "Couldn’t use the playlist: "+err.Error())
			return
		}
		out["channels"] = len(entries)
	} else if err := tv.CheckXMLTV(s.TV, name); err != nil {
		fail(400, "Couldn’t use the guide: "+err.Error())
		return
	}
	s.Log.Infof("Live TV: %s uploaded a %s (%d KB)", userOf(r).Name, kind, n>>10)
	writeJSON(w, out)
}

// removeUnusedUploads deletes uploaded files no playlist uses (older than
// an hour, so one just uploaded for a playlist not yet saved stays).
func (s *Server) removeUnusedUploads() {
	dir := s.TV.UploadDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, src := range s.St.Config().TVSources {
		used[src.PlaylistFile], used[src.GuideFile] = true, true
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && store.UploadName(e.Name()) && !used[e.Name()] && time.Since(info.ModTime()) > time.Hour {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// tvSourceChannels lists a playlist's channels for picking: those whose
// name matches q (every word) in group (when given), up to 200, with the
// playlist's groups and how many matched.
func (s *Server) tvSourceChannels(w http.ResponseWriter, r *http.Request) {
	cfg := s.St.Config()
	i := slices.IndexFunc(cfg.TVSources, func(o store.TVSource) bool { return o.ID == r.PathValue("id") })
	if i < 0 {
		writeErr(w, 404, "no such playlist")
		return
	}
	src := cfg.TVSources[i]
	entries, err := s.TV.Playlist(r.Context(), src)
	if err != nil {
		writeErr(w, 502, "Couldn’t read the playlist: "+err.Error())
		return
	}
	words := strings.Fields(strings.ToLower(r.URL.Query().Get("q")))
	// ?all=1: every match (for "select all"), not just the first page.
	limit := 200
	if r.URL.Query().Get("all") == "1" {
		limit = maxStreamsAdd
	}
	group := r.URL.Query().Get("group")
	groupSet := map[string]bool{}
	out := []tv.PlaylistEntry{}
	matched := 0
	for _, e := range entries {
		for _, g := range strings.Split(e.Group, ";") {
			if g = strings.TrimSpace(g); g != "" {
				groupSet[g] = true
			}
		}
		if group != "" && !slices.Contains(strings.Split(e.Group, ";"), group) {
			continue
		}
		name := strings.ToLower(e.Name)
		if !allIn(words, name) {
			continue
		}
		matched++
		if len(out) < limit {
			out = append(out, e)
		}
	}
	groups := make([]string, 0, len(groupSet))
	for g := range groupSet {
		groups = append(groups, g)
	}
	slices.Sort(groups)
	writeJSON(w, map[string]any{"total": len(entries), "matched": matched, "channels": out, "groups": groups})
}

func allIn(words []string, s string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
