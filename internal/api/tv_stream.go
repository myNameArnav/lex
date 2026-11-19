package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
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
// or one block for the whole window named after the channel. limit <= 0:
// all of them.
func (s *Server) streamPrograms(ch tv.Channel, from, to time.Time, limit int) []programView {
	out := []programView{}
	for _, p := range s.TV.StreamPrograms(ch, from, to) {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, programView{Item: liveItem(p.Title, p.Subtitle, p.Description), Start: p.Start.UnixMilli(), End: p.End.UnixMilli()})
	}
	if len(out) == 0 {
		out = append(out, programView{Item: liveItem(ch.Name, "", ""), Start: from.UnixMilli(), End: to.UnixMilli()})
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
	if progs := s.TV.StreamPrograms(ch, now, now.Add(12*time.Hour)); len(progs) > 0 {
		pv := func(p tv.GuideProgram) programView {
			return programView{Item: liveItem(p.Title, p.Subtitle, p.Description), Start: p.Start.UnixMilli(), End: p.End.UnixMilli()}
		}
		out["now"] = pv(progs[0])
		if len(progs) > 1 {
			out["next"] = pv(progs[1])
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
	at      time.Time
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
	if st.Referrer != "" {
		args = append(args, "-headers", "Referer: "+st.Referrer+"\r\n")
	}
	return append(args, "-i", st.URL)
}

// probeStream reads st's variants and codecs; kept for ten minutes.
func (s *Server) probeStream(ctx context.Context, st store.TVStream) (streamProbe, error) {
	key := st.URL + "|" + st.UserAgent + "|" + st.Referrer
	probeMu.Lock()
	p, ok := probeCache[key]
	probeMu.Unlock()
	if ok && time.Since(p.at) < 10*time.Minute {
		return p, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := append([]string{"-v", "error", "-show_entries", "program=program_id:program_tags=variant_bitrate:stream=index,codec_type,codec_name,width,height", "-of", "json"}, inputArgs(st)...)
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
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
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
	for _, ps := range streams {
		switch {
		case ps.CodecType == "video" && p.Video == "":
			p.Video, p.Width, p.Height = ps.CodecName, ps.Width, ps.Height
		case ps.CodecType == "audio" && p.Audio == "":
			p.Audio = ps.CodecName
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
		args = append(args, "-c:v", "copy")
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
	args = append(args, "-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "2000000", "pipe:1")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	cmd := exec.CommandContext(ctx, s.FF.Path, args...)
	var stderr bytes.Buffer
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
	defer func() {
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

// tvStreamsAdd adds live channels at the end of the lineup: one (the
// stream dialog) or several (picked from a playlist).
func (s *Server) tvStreamsAdd(w http.ResponseWriter, r *http.Request) {
	var in []store.TVStream
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad streams: "+err.Error())
		return
	}
	if len(in) == 0 || len(in) > 500 {
		writeErr(w, 400, "Add between 1 and 500 channels at a time")
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
	for i := range in {
		in[i] = in[i].Clean()
		if !store.WebURL(in[i].URL) {
			writeErr(w, 400, "Enter the playlist’s http(s) address")
			return
		}
		if in[i].GuideURL != "" && !store.WebURL(in[i].GuideURL) {
			writeErr(w, 400, "Enter the guide’s http(s) address, or leave it empty")
			return
		}
	}
	_, err := s.St.UpdateConfig(func(c *store.Config) error { c.TVSources = in; return nil })
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.writeLineup(w, "")
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
		if len(out) < 200 {
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
