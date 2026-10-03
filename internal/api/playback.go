package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lex/internal/store"
	"lex/internal/stream"
)

type planReq struct {
	stream.PlanRequest
	ItemID int64 `json:"itemId"`
}

func displayTitle(it *store.Item, show *store.Item) (string, string) {
	if it.Kind == "episode" && show != nil {
		ep := fmt.Sprintf("S%02dE%02d", it.Season, it.Episode)
		return show.Title, ep + " · " + it.Title
	}
	if it.Year > 0 {
		return it.Title, strconv.Itoa(it.Year)
	}
	return it.Title, ""
}

func (s *Server) plan(w http.ResponseWriter, r *http.Request) {
	var req planReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u := userOf(r)
	it, err := s.St.ItemForUser(req.ItemID, u.ID)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	files, err := s.St.ItemFiles(it.ID)
	if err != nil || len(files) == 0 {
		writeErr(w, 404, "no media file for this item")
		return
	}
	f := files[0]
	for _, x := range files {
		if x.ID == req.FileID {
			f = x
		}
	}
	// A file added moments ago might not be probed yet: probe it now.
	if f.Info == nil {
		info, err := s.Scanner.ProbeFile(r.Context(), f.Path)
		if err != nil {
			s.St.SaveProbe(f.ID, nil, err.Error())
			s.Log.Errorf("probe file %d: %v", f.ID, err)
			writeErr(w, 500, "could not analyse file; see admin logs")
			return
		}
		s.St.SaveProbe(f.ID, info, "")
		f.Info = info
	}
	if _, err := os.Stat(s.Cache.Resolve(f)); err != nil {
		writeErr(w, 404, "media file is missing on disk (is the drive mounted?)")
		return
	}
	if req.SessionID == "" {
		req.SessionID = store.RandomToken(12)
	}
	ip := s.clientIP(r)
	remote := s.isRemote(ip)
	cfg := s.St.Config()
	req.HLSCopy = s.HLS.CanCopy(f)
	plan, err := stream.Decide(cfg, f, req.PlanRequest, remote)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	var show *store.Item
	if it.ShowID > 0 {
		show, _ = s.St.Item(it.ShowID)
	}
	title, sub := displayTitle(it, show)
	v := f.Info.Video()
	vin, ain := "", ""
	if v != nil {
		vin = fmt.Sprintf("%s %dx%d", strings.ToUpper(v.Codec), v.Width, v.Height)
		if v.HDR != "" {
			vin += " " + v.HDR
		}
	}
	if a := f.Info.StreamByIndex(plan.Audio); a != nil {
		ain = fmt.Sprintf("%s %dch", strings.ToUpper(a.Codec), a.Channels)
		if a.Language != "" {
			ain += " " + a.Language
		}
	}
	cached := s.Cache.IsCached(f.ID)
	_, err = s.Sess.Open(&stream.Session{
		Cached: cached, HLS: plan.HLS,
		ID: plan.SessionID, UserID: u.ID, UserName: u.Name, ItemID: it.ID, FileID: f.ID, Title: title, Subtitle: sub,
		Method: plan.Method, Reasons: plan.Reasons, VideoIn: vin, AudioIn: ain, VideoOut: plan.VideoOut, AudioOut: plan.AudioOut,
		Container: stream.ContainerKey(f.Path), SrcBitrate: int(f.Info.Bitrate / 1000), OutBitrate: plan.Bitrate,
		Client: clientName(r.UserAgent()), IP: ip, Remote: remote, Duration: f.Info.Duration, Position: req.Start,
		AuthToken: s.token(r),
	})
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	s.Subs.Prefetch(f)
	s.cacheAround(it, f)
	s.Trick.Prioritize(f.ID)
	if plan.HLS {
		plan.URL += "&k=" + s.Sess.StreamKey(plan.SessionID)
	}
	s.Log.Infof("play: %s — %s %s via %s%s %v (%s)", u.Name, title, sub, plan.Method, map[bool]string{true: " (HLS)"}[plan.HLS], plan.Reasons, ip)
	writeJSON(w, map[string]any{
		"plan":     plan,
		"item":     it,
		"file":     fileView{File: fileForUser(f, u.IsAdmin), Name: filepath.Base(f.Path)},
		"title":    title,
		"subtitle": sub,
		"cached":   cached,
		"segments": s.segments(it.ID),
	})
}

func (s *Server) progress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string             `json:"sessionId"`
		FileID    int64              `json:"fileId"`
		Method    string             `json:"method"`
		Position  float64            `json:"position"`
		Paused    bool               `json:"paused"`
		Stats     stream.ClientStats `json:"stats"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	sess, err := s.Sess.Heartbeat(req.SessionID, userOf(r).ID, req.Position, req.Paused, req.Stats)
	// The server restarted while the player had the rest of the file
	// buffered, so no media request re-opened the session: re-open it here,
	// as a media request would, so progress keeps being saved.
	if errors.Is(err, stream.ErrUnknownSession) && req.SessionID != "" && req.FileID > 0 {
		if f, ferr := s.St.File(req.FileID); ferr == nil {
			method := req.Method
			if method != "remux" && method != "transcode" {
				method = "direct"
			}
			if _, oerr := s.sessionFor(r, f, req.SessionID, method); oerr == nil {
				sess, err = s.Sess.Heartbeat(req.SessionID, userOf(r).ID, req.Position, req.Paused, req.Stats)
			}
		}
	}
	if errors.Is(err, stream.ErrStopped) {
		writeErr(w, http.StatusGone, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	resp := map[string]any{"ok": true, "serverRate": sess.Rate}
	for _, snap := range s.Sess.Snapshot() {
		if snap.ID == req.SessionID && snap.Job != nil {
			resp["job"] = snap.Job
		}
	}
	writeJSON(w, resp)
}

func (s *Server) stopPlayback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string  `json:"sessionId"`
		Position  float64 `json:"position"`
	}
	// sendBeacon posts text/plain; parse regardless of content type.
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u := userOf(r)
	if sess := s.Sess.Get(req.SessionID); sess != nil && sess.UserID == u.ID && req.Position > 0 {
		s.Sess.Heartbeat(req.SessionID, u.ID, req.Position, true, sess.Client_)
	}
	s.Sess.Stop(req.SessionID, u.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

// writeSessionErr answers a request whose play session can't be used: 410
// once an admin stopped it (the player shows the message and stays stopped),
// 403 when it belongs to someone else.
func writeSessionErr(w http.ResponseWriter, err error) {
	if errors.Is(err, stream.ErrStopped) {
		writeErr(w, http.StatusGone, err.Error())
		return
	}
	writeErr(w, 403, err.Error())
}

// sessionFor finds the play session for a media request, creating a minimal
// one if the server restarted mid-playback. A session an admin stopped stays
// stopped (stream.ErrStopped).
func (s *Server) sessionFor(r *http.Request, f *store.File, sid, method string) (*stream.Session, error) {
	u := userOf(r)
	if sid == "" {
		sid = "anon-" + strconv.FormatInt(f.ID, 10) + "-" + strconv.FormatInt(u.ID, 10)
	}
	if sess, err := s.Sess.Find(sid, u.ID, f.ID); err != nil || sess != nil {
		return sess, err
	}
	it, _ := s.St.Item(f.ItemID)
	title := filepath.Base(f.Path)
	if it != nil {
		var show *store.Item
		if it.ShowID > 0 {
			show, _ = s.St.Item(it.ShowID)
		}
		title, _ = displayTitle(it, show)
	}
	ip := s.clientIP(r)
	return s.Sess.Open(&stream.Session{ID: sid, UserID: u.ID, UserName: u.Name, ItemID: f.ItemID, FileID: f.ID, Title: title,
		Method: method, Client: clientName(r.UserAgent()), IP: ip, Remote: s.isRemote(ip), Duration: f.Duration})
}

type countingWriter struct {
	http.ResponseWriter
	n func(int)
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n(n)
	return n, err
}

var directTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm", ".mkv": "video/x-matroska",
}

func (s *Server) direct(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	fh, err := os.Open(s.Cache.Resolve(f))
	if err != nil {
		writeErr(w, 404, "file missing on disk")
		return
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		s.Log.Errorf("stat file %d: %v", f.ID, err)
		writeErr(w, 500, "could not read media file")
		return
	}
	sess, err := s.sessionFor(r, f, r.URL.Query().Get("sid"), "direct")
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	s.Sess.Touch(sess, 1)
	defer s.Sess.Touch(sess, -1)
	if ct, ok := directTypes[strings.ToLower(filepath.Ext(f.Path))]; ok {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "private, no-transform")
	cw := &countingWriter{ResponseWriter: w, n: func(n int) { s.Sess.AddBytes(sess, n) }}
	// An admin Stop ends the session: cut this response off too, so the
	// browser can't keep playing from the open connection. The deadline
	// fails a write blocked on a full socket; endedReader stops the copy.
	if ended := sess.Ended(); ended != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ended:
				// Not if the response finished meanwhile: the deadline
				// would fail the connection's next keep-alive request.
				select {
				case <-done:
				default:
					_ = http.NewResponseController(w).SetWriteDeadline(time.Now())
				}
			case <-done:
			}
		}()
		http.ServeContent(cw, r, "", st.ModTime(), &endedReader{ReadSeeker: fh, ended: ended})
		return
	}
	http.ServeContent(cw, r, "", st.ModTime(), fh)
}

// endedReader fails reads once its session has ended.
type endedReader struct {
	io.ReadSeeker
	ended <-chan struct{}
}

func (e *endedReader) Read(b []byte) (int, error) {
	select {
	case <-e.ended:
		return 0, stream.ErrStopped
	default:
		return e.ReadSeeker.Read(b)
	}
}

// streamFile runs ffmpeg and pipes fragmented MP4 to the client. Backpressure
// is natural: when the player stops reading (buffer full), the socket fills,
// ffmpeg blocks on the pipe and uses no CPU.
func (s *Server) streamFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	cfg := s.St.Config()
	p := stream.ParseParams(f.ID, r.URL.Query())
	if !p.VideoCopy && !cfg.EnableTranscode {
		writeErr(w, 403, "transcoding is disabled")
		return
	}
	if !cfg.EnableRemux && !cfg.EnableTranscode {
		writeErr(w, 403, "remuxing is disabled")
		return
	}
	if p.Duration(f) > 0 && p.Start >= p.Duration(f) {
		writeErr(w, 416, "start past end of file")
		return
	}
	p.HW = cfg.HWDecode && s.FF.HEVCHWDecode
	p.Input = s.Cache.Resolve(f)
	args, err := stream.BuildArgs(cfg, s.FF, f, p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if stream.HWEncode(cfg, p) && !s.FF.V4L2Device {
		// Encoder configured but the device is gone (e.g. after a reboot
		// with the codec blacklisted): fall back to software.
		cfg.VideoEncoder = "libx264"
		args, _ = stream.BuildArgs(cfg, s.FF, f, p)
	}
	method := "remux"
	if !p.VideoCopy {
		method = "transcode"
	}
	sess, err := s.sessionFor(r, f, p.SessionID, method)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	job, err := s.Sess.AttachJob(sess, cfg.MaxTranscodes, !p.VideoCopy, func() (*stream.Job, error) {
		return stream.StartJob(s.FF.Path, args, p, cfg.FFmpegNice)
	})
	if err != nil {
		if errors.Is(err, stream.ErrLimit) {
			writeErr(w, 503, err.Error())
			return
		}
		s.Log.Errorf("ffmpeg start for file %d: %v", f.ID, err)
		writeErr(w, 500, "ffmpeg could not start; see admin logs")
		return
	}
	s.Log.Debugf("ffmpeg %s | %s", strings.Join(args.Args, " "), strings.Join(args.Stage2, " "))
	s.Sess.Touch(sess, 1)
	defer s.Sess.Touch(sess, -1)
	defer s.Sess.DetachJob(sess, job)
	defer job.Stop()
	defer job.Stdout.Close()

	// Stop ffmpeg as soon as the client goes away.
	go func(j *stream.Job) {
		select {
		case <-r.Context().Done():
			j.Stop()
		case <-j.Done():
		}
	}(job)

	buf := make([]byte, cfg.StreamBufferKB*1024)
	// Wait for the first bytes before committing to a 200, so ffmpeg
	// start-up errors can be reported properly.
	n, rerr := io.ReadAtLeast(job.Stdout, buf[:min(len(buf), 4096)], 1)
	if n == 0 && p.HW && r.Context().Err() == nil {
		// Hardware decode failed to start: retry in software once.
		<-job.Done()
		s.Log.Warnf("hardware decoding failed for %s (%s); retrying in software", filepath.Base(f.Path), strings.TrimSpace(job.StderrTail()))
		job.Stdout.Close()
		p.HW = false
		if args, err = stream.BuildArgs(cfg, s.FF, f, p); err == nil {
			job, err = s.Sess.AttachJob(sess, cfg.MaxTranscodes, !p.VideoCopy, func() (*stream.Job, error) {
				return stream.StartJob(s.FF.Path, args, p, cfg.FFmpegNice)
			})
		}
		if err != nil {
			s.Log.Errorf("ffmpeg retry for file %d: %v", f.ID, err)
			writeErr(w, 500, "ffmpeg could not start; see admin logs")
			return
		}
		defer s.Sess.DetachJob(sess, job)
		defer job.Stop()
		defer job.Stdout.Close()
		go func(j *stream.Job) {
			select {
			case <-r.Context().Done():
				j.Stop()
			case <-j.Done():
			}
		}(job)
		n, rerr = io.ReadAtLeast(job.Stdout, buf[:min(len(buf), 4096)], 1)
	}
	if n == 0 {
		<-job.Done()
		msg := strings.TrimSpace(job.StderrTail())
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		if r.Context().Err() == nil {
			s.Log.Errorf("ffmpeg failed for %s: %s", filepath.Base(f.Path), msg)
			writeErr(w, 500, "ffmpeg failed; see admin logs")
		}
		return
	}
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Start-Time", strconv.FormatFloat(p.Start, 'f', 3, 64))
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)
	rc := http.NewResponseController(w)
	write := func(b []byte) bool {
		// A client that stops reading for too long is gone.
		rc.SetWriteDeadline(time.Now().Add(2 * time.Minute))
		if _, err := w.Write(b); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		s.Sess.AddBytes(sess, len(b))
		return true
	}
	if !write(buf[:n]) {
		return
	}
	for {
		n, err := job.Stdout.Read(buf)
		if n > 0 && !write(buf[:n]) {
			return
		}
		if err != nil {
			break
		}
	}
	<-job.Done()
	if pr := job.Progress(); pr.Error != "" && r.Context().Err() == nil {
		s.Log.Warnf("ffmpeg exited with error for %s: %s %s", filepath.Base(f.Path), pr.Error, job.StderrTail())
	}
}

func (s *Server) subtitle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	raw := r.PathValue("idx")
	wantASS := strings.HasSuffix(raw, ".ass")
	idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(raw, ".vtt"), ".ass"))
	if err != nil {
		writeErr(w, 400, "bad index")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	var p string
	var partial []byte
	format := "vtt"
	if wantASS {
		format = "ass"
	}
	switch {
	case idx < 1000:
		// Embedded: may still be extracting; then send what's ready and let
		// the player come back for the rest.
		p, partial, err = s.Subs.Serve(r.Context(), f, idx, format, 3*time.Second)
	case wantASS:
		p, err = s.Subs.GetASS(r.Context(), f, idx)
	case idx >= store.DownloadedSubBase:
		var d *store.DownloadedSub
		if d, err = s.St.DownloadedSub(int64(idx - store.DownloadedSubBase)); err == nil && d.FileID == f.ID {
			p, err = s.Subs.GetFile(r.Context(), f, idx, d.Path)
		} else if err == nil {
			err = store.ErrNotFound
		}
	default:
		p, err = s.Subs.Get(r.Context(), f, idx)
	}
	if err != nil {
		if r.Context().Err() == nil {
			s.Log.Errorf("subtitle file %d/%d: %v", f.ID, idx, err)
			writeErr(w, 500, "could not extract subtitles; see admin logs")
		}
		return
	}
	if wantASS {
		w.Header().Set("Content-Type", "text/x-ssa; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	}
	if partial != nil {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Lex-Partial", "1")
		w.Write(partial)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, p)
}

// cacheAround queues the file being played, and the next few episodes, for
// the SSD cache.
func (s *Server) cacheAround(it *store.Item, f *store.File) {
	cfg := s.St.Config()
	if !cfg.CacheEnabled {
		return
	}
	if cfg.CacheOnPlay {
		s.Cache.Request(f, "playing")
	}
	if it.Kind != "episode" || cfg.CachePrefetch <= 0 {
		return
	}
	eps, err := s.St.ShowEpisodes(it.ShowID, 0)
	if err != nil {
		return
	}
	for i, e := range eps {
		if e.ID != it.ID {
			continue
		}
		for j := i + 1; j < len(eps) && j <= i+cfg.CachePrefetch; j++ {
			if files, err := s.St.ItemFiles(eps[j].ID); err == nil && len(files) > 0 {
				s.Cache.Request(files[0], "next episode")
			}
		}
		break
	}
}
