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
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"regexp"
	"slices"
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
	"lex/internal/tv"
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
	TV         *tv.Guide // Live TV; New(St) if nil
	Web        fs.FS
	ListenAddr string // -addr, used to suggest LAN webhook URLs

	static    map[string]*asset
	buildID   string // content hash of the UI files; assets are also served under /b/<buildID>/
	loginMu   sync.Mutex
	loginFail map[string]*failWindow
	thumbSem  chan struct{}
	resizeSem chan struct{}
	// Files whose subtitle prefetch waits for their SSD cache copy
	// (prefetchSubs).
	subsAfterCacheMu sync.Mutex
	subsAfterCache   map[int64]bool
	backupMu         sync.Mutex // one backup at a time; guards backupErr
	backupErr        string     // why the last scheduled backup failed ("" if it didn't)
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
	if s.TV == nil {
		s.TV = tv.New(s.St)
	}
	if err := s.loadStatic(); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/public/info", s.publicInfo)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	// Sonarr/Radarr let the user pick POST or PUT for webhooks.
	mux.HandleFunc("POST /api/webhook/scan", s.webhookScan)
	mux.HandleFunc("PUT /api/webhook/scan", s.webhookScan)

	// Signed-in users
	u := func(h http.HandlerFunc) http.Handler { return s.auth(h, false) }
	mux.Handle("GET /api/me", u(s.me))
	mux.Handle("PUT /api/me/prefs", u(s.savePrefs))
	mux.Handle("PUT /api/me/password", u(s.changePassword))
	mux.Handle("GET /api/libraries", u(s.libraries))
	mux.Handle("GET /api/home", u(s.home))
	mux.Handle("GET /api/items", u(s.listItems))
	mux.Handle("GET /api/genres", u(s.genres))
	mux.Handle("GET /api/letters", u(s.letters))
	mux.Handle("GET /api/search", u(s.search))
	mux.Handle("GET /api/items/{id}", u(s.itemDetail))
	mux.Handle("GET /api/items/{id}/children", u(s.itemChildren))
	mux.Handle("GET /api/items/{id}/shuffle", u(s.shuffle))
	mux.Handle("GET /api/items/{id}/image/{kind}", u(s.itemImage))
	mux.Handle("GET /api/items/{id}/cast/{index}/image", u(s.castImage))
	mux.Handle("POST /api/items/{id}/played", u(s.setPlayed))
	mux.Handle("POST /api/items/{id}/favorite", u(s.setFavorite))
	mux.Handle("POST /api/items/{id}/watchlist", u(s.setWatchlist))
	mux.Handle("GET /api/watchlist", u(s.watchlist))
	mux.Handle("GET /api/favorites", u(s.favorites))
	mux.Handle("GET /api/collections/{id}", u(s.collection))
	mux.Handle("GET /api/person", u(s.person))
	mux.Handle("GET /api/tv", u(s.liveTV(s.tvGuide)))
	mux.Handle("GET /api/tv/{id}", u(s.liveTV(s.tvChannel)))
	mux.Handle("POST /api/playback/plan", u(s.plan))
	mux.Handle("POST /api/items/{id}/compat", u(s.itemCompat))
	mux.Handle("POST /api/playback/progress", u(s.progress))
	mux.Handle("POST /api/playback/stop", u(s.stopPlayback))
	mux.Handle("POST /api/playback/event", u(s.playbackEvent))
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
	mux.Handle("POST /api/admin/arr/{app}/status", a(s.arrStatus))
	mux.Handle("POST /api/admin/arr/{app}/connect", a(s.arrConnect))
	mux.Handle("GET /api/admin/tv/automatic", a(s.tvAutomatic))
	mux.Handle("GET /api/admin/libraries", a(s.adminLibraries))
	mux.Handle("POST /api/admin/libraries", a(s.createLibrary))
	mux.Handle("PUT /api/admin/libraries/{id}", a(s.updateLibrary))
	mux.Handle("DELETE /api/admin/libraries/{id}", a(s.deleteLibrary))
	mux.Handle("POST /api/admin/scan", a(s.scan))
	mux.Handle("GET /api/admin/tasks", a(s.tasks))
	mux.Handle("POST /api/admin/metadata/refresh", a(s.refreshAllMetadata))
	mux.Handle("GET /api/admin/metadata/search", a(s.metaSearch))
	mux.Handle("GET /api/admin/metadata/poster", a(s.metadataPoster))
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
	mux.Handle("POST /api/admin/compat", a(s.compat))
	mux.Handle("POST /api/admin/compat/titles", a(s.compatTitles))
	mux.Handle("GET /api/admin/stats/playback", a(s.statsPlayback))
	mux.Handle("GET /api/admin/history", a(s.history))
	mux.Handle("DELETE /api/admin/history", a(s.clearHistory))
	mux.Handle("GET /api/admin/logs", a(s.logs))
	mux.Handle("POST /api/admin/cache/clear", a(s.clearCache))
	mux.Handle("GET /api/admin/backups", a(s.listBackups))
	mux.Handle("POST /api/admin/backups", a(s.createBackup))
	mux.Handle("GET /api/admin/backups/{name}", a(s.downloadBackup))
	mux.Handle("DELETE /api/admin/backups/{name}", a(s.deleteBackup))
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
		// API responses may get gzip; gzipWriter only compresses text-like
		// content types, so images and media pass through untouched.
		if strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/files/") &&
			strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
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
		if code == http.StatusNoContent || code == http.StatusNotModified || code == http.StatusPartialContent ||
			g.Header().Get("Content-Encoding") != "" || !compressible(g.Header().Get("Content-Type")) {
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

// compressible reports whether a response of this Content-Type is worth
// gzipping: JSON and text yes, images and media (already compressed) no.
func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.HasPrefix(ct, "text/"), strings.HasSuffix(ct, "json"), strings.HasSuffix(ct, "+xml"),
		ct == "application/javascript", ct == "application/xml",
		ct == "application/vnd.apple.mpegurl", ct == "application/x-mpegurl":
		return true
	}
	return false
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.started {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
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
	return cfg.TrustProxy && inCIDRs(cfg.TrustedProxies, net.ParseIP(peerIP(r)))
}

func inCIDRs(list string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, cidr := range strings.Split(list, ",") {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP identifies the client. Behind a trusted proxy it walks
// X-Forwarded-For from the right: each proxy appends the address it saw, so
// the rightmost entry that isn't itself a trusted proxy is the first one a
// client couldn't forge. Entries to its left, and headers such as X-Real-IP
// or CF-Connecting-IP that proxies often pass through untouched, are ignored.
func (s *Server) clientIP(r *http.Request) string {
	peer := peerIP(r)
	if !s.trustedProxy(r) {
		return peer
	}
	proxies := s.St.Config().TrustedProxies
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if host, _, err := net.SplitHostPort(hop); err == nil {
			hop = host
		}
		ip := net.ParseIP(hop)
		if ip == nil {
			// Unreadable hop: nothing left of it can be trusted.
			break
		}
		client = ip.String()
		if !inCIDRs(proxies, ip) {
			break
		}
	}
	return client
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
	err := fs.WalkDir(s.Web, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(s.Web, p)
		if err != nil {
			return err
		}
		s.static["/"+p] = newAsset(p, b)
		return nil
	})
	if err != nil {
		return err
	}
	s.buildID = s.computeBuildID()
	s.preloadModules()
	s.versionPages()
	return s.stampServiceWorker()
}

// Long-lived caching (#66). Every file is also served under
// /b/<build id>/<path>, where the build id is a hash of all the UI files, as
// immutable: the browser keeps it for a year without asking again. The HTML
// pages load the stylesheet and modules from there, and the modules import
// each other (and the wasm bundles) by relative paths, so the whole graph
// stays under the same prefix. A new build gets a new prefix; only the
// pages and sw.js are revalidated on each load.
const buildPrefix = "/b/"

// computeBuildID hashes every UI file (and nothing else), so an unchanged UI
// keeps its id across restarts and server-only releases.
func (s *Server) computeBuildID() string {
	paths := make([]string, 0, len(s.static))
	for p := range s.static {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	h := sha1.New()
	for _, p := range paths {
		io.WriteString(h, p+"\x00"+s.static[p].etag+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}

// versioned is p under this build's prefix.
func (s *Server) versioned(p string) string { return buildPrefix + s.buildID + p }

// isVersionedAsset reports whether p is loaded from the build prefix: the
// stylesheet and scripts. Icons, the manifest and the pages keep fixed URLs.
func isVersionedAsset(p string) bool {
	return strings.HasPrefix(p, "/js/") || strings.HasPrefix(p, "/css/") || strings.HasPrefix(p, "/vendor/")
}

// versionPages points the HTML pages' stylesheet, script and modulepreload
// URLs at the build prefix.
func (s *Server) versionPages() {
	for _, page := range []string{"/index.html", "/offline.html"} {
		a := s.static[page]
		if a == nil {
			continue
		}
		body := a.body
		for _, dir := range []string{"/js/", "/css/", "/vendor/"} {
			body = bytes.ReplaceAll(body, []byte(`="`+dir), []byte(`="`+s.versioned(dir)))
		}
		s.static[page] = newAsset(page[1:], body)
	}
}

var (
	// entryModuleRe finds the page's module script; staticImportRe the
	// specifier of each static import (or re-export) in a module. Dynamic
	// import() calls are left alone: those load on demand.
	entryModuleRe  = regexp.MustCompile(`<script type="module" src="([^"]+)"></script>`)
	staticImportRe = regexp.MustCompile(`(?m)^\s*(?:import|export)\s+(?:[^'";]*?\s+from\s+)?['"]([^'"]+)['"]`)
)

// moduleGraph lists the modules entry imports statically, directly or
// through other modules, in the order found (entry itself left out).
func (s *Server) moduleGraph(entry string) []string {
	seen := map[string]bool{entry: true}
	var out []string
	for queue := []string{entry}; len(queue) > 0; queue = queue[1:] {
		a := s.static[queue[0]]
		if a == nil {
			continue
		}
		for _, m := range staticImportRe.FindAllSubmatch(a.body, -1) {
			spec := string(m[1])
			if !strings.HasPrefix(spec, "/") {
				spec = path.Join(path.Dir(queue[0]), spec)
			}
			if seen[spec] || s.static[spec] == nil {
				continue
			}
			seen[spec] = true
			out = append(out, spec)
			queue = append(queue, spec)
		}
	}
	return out
}

// preloadModules adds a modulepreload link to index.html for every module
// the page's script imports. The UI is unbundled modules, which a browser
// otherwise finds one import level per round trip; with the links it asks
// for them all at once.
func (s *Server) preloadModules() {
	index := s.static["/index.html"]
	if index == nil {
		return
	}
	loc := entryModuleRe.FindSubmatchIndex(index.body)
	if loc == nil {
		return
	}
	var links bytes.Buffer
	for _, m := range s.moduleGraph(string(index.body[loc[2]:loc[3]])) {
		links.WriteString(`<link rel="modulepreload" href="` + html.EscapeString(m) + `">` + "\n")
	}
	if links.Len() == 0 {
		return
	}
	body := slices.Concat(index.body[:loc[0]], links.Bytes(), index.body[loc[0]:])
	s.static["/index.html"] = newAsset("index.html", body)
}

func newAsset(p string, b []byte) *asset {
	sum := sha1.Sum(b)
	a := &asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: mime.TypeByExtension(path.Ext(p))}
	if a.ctype == "" {
		a.ctype = "application/octet-stream"
	}
	if strings.HasPrefix(a.ctype, "text/") || strings.Contains(a.ctype, "javascript") || strings.Contains(a.ctype, "json") || strings.Contains(a.ctype, "svg") ||
		a.ctype == "application/wasm" {
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		gz.Write(b)
		gz.Close()
		if buf.Len() < len(b) {
			a.gz = buf.Bytes()
		}
	}
	return a
}

// swBuildMarker is replaced in sw.js with this build's version and shell list.
const swBuildMarker = "/*LEX_BUILD*/null"

// shellPaths lists the files the service worker precaches: the HTML shell
// (as "/"), stylesheet, manifest, icons and the /js modules. The wasm vendor
// bundles, sw.js itself and anything under /api/ are never cached.
func (s *Server) shellPaths() []string {
	shell := []string{"/"}
	for p := range s.static {
		switch {
		case p == "/index.html", p == "/sw.js", strings.HasPrefix(p, "/vendor/"):
		case strings.HasPrefix(p, "/js/") && path.Ext(p) == ".js", strings.HasPrefix(p, "/css/"):
			// What the pages load: from the build prefix.
			shell = append(shell, s.versioned(p))
		case strings.HasPrefix(p, "/icons/"), p == "/favicon.svg", p == "/manifest.json", p == "/offline.html":
			shell = append(shell, p)
		}
	}
	slices.Sort(shell[1:])
	return shell
}

// stampServiceWorker writes the build into sw.js: a version derived from the
// server version and the shell files' hashes, so any UI change installs a
// new worker (and cache), and the list of files to precache.
func (s *Server) stampServiceWorker() error {
	sw := s.static["/sw.js"]
	if sw == nil {
		return nil
	}
	if !bytes.Contains(sw.body, []byte(swBuildMarker)) {
		return errors.New("sw.js: build marker missing")
	}
	shell := s.shellPaths()
	h := sha1.New()
	io.WriteString(h, s.Version)
	for _, p := range shell {
		a := s.static[strings.TrimPrefix(p, buildPrefix+s.buildID)]
		if p == "/" {
			a = s.static["/index.html"]
		}
		if a != nil {
			io.WriteString(h, "\x00"+p+"\x00"+a.etag)
		}
	}
	build, err := json.Marshal(struct {
		Version string   `json:"version"`
		Shell   []string `json:"shell"`
	}{hex.EncodeToString(h.Sum(nil)[:6]), shell})
	if err != nil {
		return err
	}
	s.static["/sw.js"] = newAsset("sw.js", bytes.Replace(sw.body, []byte(swBuildMarker), build, 1))
	return nil
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		writeErr(w, 405, "method not allowed")
		return
	}
	p := r.URL.Path
	// /b/<build id>/<path>: this build's files are immutable. A page from an
	// older build may still ask for its own id; it gets today's file, but
	// without the long cache, so the stale combination doesn't stick.
	immutable := false
	if rest, found := strings.CutPrefix(p, buildPrefix); found {
		id, file, _ := strings.Cut(rest, "/")
		file = "/" + file
		if !isVersionedAsset(file) {
			http.NotFound(w, r)
			return
		}
		immutable = id == s.buildID
		p = file
	}
	a, ok := s.static[p]
	if !ok && path.Ext(p) != "" {
		// A missing file (e.g. iOS probing /apple-touch-icon.png) is a 404,
		// not the app shell: the UI routes with the hash, never the path.
		http.NotFound(w, r)
		return
	}
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
	if immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
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
