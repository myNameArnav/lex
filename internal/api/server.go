// Package api wires HTTP routes to the library, metadata and streaming layers.
package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/cache"
	"lex/internal/intro"
	"lex/internal/library"
	"lex/internal/logx"
	"lex/internal/meta"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/subsearch"
	"lex/internal/sysstats"
	"lex/internal/trickplay"
)

type Server struct {
	Version    string
	DataDir    string
	St         *store.Store
	Log        *logx.Logger
	Scanner    *library.Scanner
	Agent      *meta.Agent
	Images     *meta.ImageCache
	Subs       *stream.Subs
	Sess       *stream.Manager
	Stats      *sysstats.Sampler
	FF         stream.FFInfo
	subMu      sync.Mutex
	subsClient *subsearch.Client
	Cache      *cache.Cache
	Intro      *intro.Detector
	Trick      *trickplay.Generator
	HLS        *stream.HLS
	Web        fs.FS

	static    map[string]*asset
	loginMu   sync.Mutex
	loginFail map[string]*failWindow
	thumbSem  chan struct{}
	resizeSem chan struct{}
}

type asset struct {
	body  []byte
	gz    []byte
	etag  string
	ctype string
}

type failWindow struct {
	n     int
	start time.Time
}

type ctxKey int

const userKey ctxKey = 1

func (s *Server) Handler() (http.Handler, error) {
	s.loginFail = map[string]*failWindow{}
	s.thumbSem = make(chan struct{}, 1)
	s.resizeSem = make(chan struct{}, 2)
	if err := s.loadStatic(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/public/info", s.publicInfo)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("POST /api/webhook/scan", s.webhookScan)

	// Signed-in users
	u := func(h http.HandlerFunc) http.Handler { return s.auth(h, false) }
	mux.Handle("GET /api/me", u(s.me))
	mux.Handle("PUT /api/me/prefs", u(s.savePrefs))
	mux.Handle("PUT /api/me/password", u(s.changePassword))
	mux.Handle("GET /api/libraries", u(s.libraries))
	mux.Handle("GET /api/home", u(s.home))
	mux.Handle("GET /api/items", u(s.listItems))
	mux.Handle("GET /api/genres", u(s.genres))
	mux.Handle("GET /api/search", u(s.search))
	mux.Handle("GET /api/items/{id}", u(s.itemDetail))
	mux.Handle("GET /api/items/{id}/children", u(s.itemChildren))
	mux.Handle("GET /api/items/{id}/image/{kind}", u(s.itemImage))
	mux.Handle("GET /api/items/{id}/cast/{index}/image", u(s.castImage))
	mux.Handle("POST /api/items/{id}/played", u(s.setPlayed))
	mux.Handle("POST /api/items/{id}/favorite", u(s.setFavorite))
	mux.Handle("POST /api/playback/plan", u(s.plan))
	mux.Handle("POST /api/playback/progress", u(s.progress))
	mux.Handle("POST /api/playback/stop", u(s.stopPlayback))
	mux.Handle("GET /api/files/{id}/direct", u(s.direct))
	mux.Handle("HEAD /api/files/{id}/direct", u(s.direct))
	mux.Handle("GET /api/files/{id}/stream", u(s.streamFile))
	mux.Handle("GET /api/files/{id}/subs/{idx}", u(s.subtitle))
	mux.Handle("GET /api/files/{id}/subsearch", u(s.subSearch))
	mux.Handle("POST /api/files/{id}/subsearch", u(s.subDownload))
	mux.Handle("DELETE /api/files/{id}/subs/{idx}", u(s.subDelete))
	mux.Handle("GET /api/files/{id}/fonts", u(s.fonts))
	mux.Handle("GET /api/files/{id}/trickplay", u(s.trickMeta))
	mux.Handle("GET /api/files/{id}/hls/index.m3u8", s.hlsAuth(s.hlsPlaylist))
	mux.Handle("GET /api/files/{id}/hls/init.mp4", s.hlsAuth(s.hlsSegment))
	mux.Handle("GET /api/files/{id}/hls/seg/{n}", s.hlsAuth(s.hlsSegment))
	mux.Handle("GET /api/files/{id}/trickplay/{n}", u(s.trickSheet))
	mux.Handle("GET /api/files/{id}/fonts/{name}", u(s.font))

	// Admin
	a := func(h http.HandlerFunc) http.Handler { return s.auth(h, true) }
	mux.Handle("GET /api/admin/config", a(s.getConfig))
	mux.Handle("PUT /api/admin/config", a(s.putConfig))
	mux.Handle("GET /api/admin/info", a(s.adminInfo))
	mux.Handle("GET /api/admin/libraries", a(s.libraries))
	mux.Handle("POST /api/admin/libraries", a(s.createLibrary))
	mux.Handle("PUT /api/admin/libraries/{id}", a(s.updateLibrary))
	mux.Handle("DELETE /api/admin/libraries/{id}", a(s.deleteLibrary))
	mux.Handle("POST /api/admin/scan", a(s.scan))
	mux.Handle("GET /api/admin/tasks", a(s.tasks))
	mux.Handle("POST /api/admin/metadata/refresh", a(s.refreshAllMetadata))
	mux.Handle("GET /api/admin/metadata/search", a(s.metaSearch))
	mux.Handle("POST /api/items/{id}/refresh", a(s.refreshItem))
	mux.Handle("POST /api/items/{id}/match", a(s.matchItem))
	mux.Handle("POST /api/items/{id}/unmatch", a(s.unmatchItem))
	mux.Handle("GET /api/admin/fs", a(s.browseFS))
	mux.Handle("GET /api/admin/users", a(s.listUsers))
	mux.Handle("POST /api/admin/users", a(s.createUser))
	mux.Handle("PUT /api/admin/users/{id}", a(s.updateUser))
	mux.Handle("DELETE /api/admin/users/{id}", a(s.deleteUser))
	mux.Handle("GET /api/admin/devices", a(s.listDevices))
	mux.Handle("DELETE /api/admin/devices/{prefix}", a(s.deleteDevice))
	mux.Handle("GET /api/admin/stats/system", a(s.statsSystem))
	mux.Handle("GET /api/admin/stats/sessions", a(s.statsSessions))
	mux.Handle("DELETE /api/admin/sessions/{id}", a(s.killSession))
	mux.Handle("GET /api/admin/stats/library", a(s.statsLibrary))
	mux.Handle("GET /api/admin/stats/library/titles", a(s.statsLibraryTitles))
	mux.Handle("GET /api/admin/stats/playback", a(s.statsPlayback))
	mux.Handle("GET /api/admin/history", a(s.history))
	mux.Handle("DELETE /api/admin/history", a(s.clearHistory))
	mux.Handle("GET /api/admin/logs", a(s.logs))
	mux.Handle("POST /api/admin/cache/clear", a(s.clearCache))
	mux.Handle("GET /api/admin/cache", a(s.cacheStatus))
	mux.Handle("POST /api/admin/cache/items/{id}", a(s.cacheItem))
	mux.Handle("DELETE /api/admin/cache/items/{id}", a(s.uncacheItem))
	mux.Handle("POST /api/admin/intro/scan", a(s.introScan))
	mux.Handle("POST /api/items/{id}/intro/reset", a(s.introReset))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/", s.serveStatic)
	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusForbidden, "cross-origin request rejected")
	}))
	return s.middleware(csrf.Handler(mux)), nil
}

// ---- middleware ----

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; object-src 'none'; base-uri 'self'; frame-ancestors 'self'")
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.Log.Errorf("panic %s %s: %v", r.Method, r.URL.Path, rec)
				writeErr(w, 500, "internal error")
			}
		}()
		// JSON API responses get gzip; media and images don't.
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/files/") &&
			!strings.Contains(r.URL.Path, "/image/") && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gw := &gzipWriter{ResponseWriter: w}
			defer gw.Close()
			w = gw
		}
		next.ServeHTTP(w, r)
	})
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, 5); return w }}

type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	started bool
	skip    bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if !g.started {
		g.started = true
		if code == http.StatusNoContent || code == http.StatusNotModified || g.Header().Get("Content-Encoding") != "" {
			g.skip = true
		} else {
			g.Header().Del("Content-Length")
			g.Header().Set("Content-Encoding", "gzip")
			g.Header().Add("Vary", "Accept-Encoding")
			g.gz = gzPool.Get().(*gzip.Writer)
			g.gz.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.started {
		g.WriteHeader(200)
	}
	if g.skip || g.gz == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipWriter) Close() {
	if g.gz != nil {
		g.gz.Close()
		gzPool.Put(g.gz)
		g.gz = nil
	}
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) token(r *http.Request) string {
	if c, err := r.Cookie("lex_token"); err == nil && c.Value != "" {
		return c.Value
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if t := r.Header.Get("X-Lex-Token"); t != "" {
		return t
	}
	return ""
}

func (s *Server) auth(next http.HandlerFunc, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.St.TokenUser(s.token(r))
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if admin && !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func userOf(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
}

// Only explicitly configured proxy peers may supply client identity or TLS headers.
func (s *Server) trustedProxy(r *http.Request) bool {
	cfg := s.St.Config()
	if !cfg.TrustProxy {
		return false
	}
	ip := net.ParseIP(peerIP(r))
	if ip == nil {
		return false
	}
	for _, cidr := range strings.Split(cfg.TrustedProxies, ",") {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) clientIP(r *http.Request) string {
	if s.trustedProxy(r) {
		for _, h := range []string{"CF-Connecting-IP", "X-Real-IP", "X-Forwarded-For"} {
			if v := r.Header.Get(h); v != "" {
				first := strings.TrimSpace(strings.Split(v, ",")[0])
				if net.ParseIP(first) != nil {
					return first
				}
			}
		}
	}
	return peerIP(r)
}

func (s *Server) isRemote(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}
	for _, c := range strings.Split(s.St.Config().LocalNetworks, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

func (s *Server) isHTTPS(r *http.Request) bool {
	return r.TLS != nil || (s.trustedProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("request body too large")
	}
	return json.Unmarshal(b, v)
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

func qInt(r *http.Request, k string, d int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil {
		return v
	}
	return d
}

func notFoundOr500(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not found")
		return
	}
	writeErr(w, 500, err.Error())
}

// ---- static files ----

func (s *Server) loadStatic() error {
	s.static = map[string]*asset{}
	return fs.WalkDir(s.Web, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(s.Web, p)
		if err != nil {
			return err
		}
		sum := sha1.Sum(b)
		a := &asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: mime.TypeByExtension(path.Ext(p))}
		if a.ctype == "" {
			a.ctype = "application/octet-stream"
		}
		if strings.HasPrefix(a.ctype, "text/") || strings.Contains(a.ctype, "javascript") || strings.Contains(a.ctype, "json") || strings.Contains(a.ctype, "svg") {
			var buf bytes.Buffer
			gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			gz.Write(b)
			gz.Close()
			if buf.Len() < len(b) {
				a.gz = buf.Bytes()
			}
		}
		s.static["/"+p] = a
		return nil
	})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeErr(w, 405, "method not allowed")
		return
	}
	p := r.URL.Path
	a, ok := s.static[p]
	if !ok || p == "/" {
		// SPA: unknown paths get the app shell.
		a = s.static["/index.html"]
		if a == nil {
			http.NotFound(w, r)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	h.Set("Cache-Control", "no-cache")
	h.Add("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.body
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == "HEAD" {
		return
	}
	w.Write(body)
}
