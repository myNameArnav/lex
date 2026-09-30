package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"lex/internal/store"
	"lex/internal/stream"
)

// hlsAuth accepts the normal session cookie/token, or the per-playback
// stream key (native HLS players don't always send cookies).
func (s *Server) hlsAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.St.TokenUser(s.token(r))
		if err != nil {
			q := r.URL.Query()
			if uid := s.Sess.KeyUser(q.Get("sid"), q.Get("k")); uid > 0 {
				u, err = s.St.User(uid)
			}
		}
		if err != nil || u == nil {
			writeErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (s *Server) hlsFile(w http.ResponseWriter, r *http.Request) (*store.File, stream.Params, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return nil, stream.Params{}, false
	}
	f, err := s.St.File(id)
	if err != nil || f.Info == nil {
		writeErr(w, 404, "not found")
		return nil, stream.Params{}, false
	}
	p := stream.ParseParams(f.ID, r.URL.Query())
	return f, p, true
}

func (s *Server) hlsPlaylist(w http.ResponseWriter, r *http.Request) {
	f, p, ok := s.hlsFile(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	q.Del("t")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(stream.Playlist(f, p, q)))
}

func (s *Server) hlsSegment(w http.ResponseWriter, r *http.Request) {
	f, p, ok := s.hlsFile(w, r)
	if !ok {
		return
	}
	cfg := s.St.Config()
	if !p.VideoCopy && !cfg.EnableTranscode {
		writeErr(w, 403, "transcoding is disabled")
		return
	}
	n := -1 // init segment
	ctype := "video/mp4"
	if name := r.PathValue("n"); name != "" {
		ext := ".ts"
		if strings.HasSuffix(name, ".m4s") {
			ext = ".m4s"
		} else {
			ctype = "video/mp2t"
		}
		v, err := strconv.Atoi(strings.TrimSuffix(name, ext))
		if err != nil || v < 0 {
			writeErr(w, 400, "bad segment")
			return
		}
		n = v
	}
	if !p.VideoCopy && cfg.MaxTranscodes > 0 {
		_, tx := s.Sess.ActiveJobs()
		if tx+s.HLS.ActiveTranscodesExcept(p.SessionID) >= cfg.MaxTranscodes {
			writeErr(w, 503, stream.ErrLimit.Error())
			return
		}
	}
	method := "remux"
	if !p.VideoCopy {
		method = "transcode"
	}
	sess, err := s.sessionFor(r, f, p.SessionID, method)
	if err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	s.Sess.Touch(sess, 1)
	defer s.Sess.Touch(sess, -1)
	p.HW = cfg.HWDecode && s.FF.HEVCHWDecode
	p.Input = s.Cache.Resolve(f)
	if !s.FF.V4L2Device && cfg.VideoEncoder == "h264_v4l2m2m" {
		cfg.VideoEncoder = "libx264"
	}
	path, err := s.HLS.Segment(r.Context(), cfg, s.FF, f, p, n, cfg.FFmpegNice)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.Log.Warnf("hls %s seg %d: %v", f.Path, n, err)
		writeErr(w, 500, "could not produce segment")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=300")
	cw := &countingWriter{ResponseWriter: w, n: func(n int) { s.Sess.AddBytes(sess, n) }}
	http.ServeFile(cw, r, path)
}
