package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lex/internal/arr"
)

// arrRequest names the Sonarr/Radarr to talk to. URL and key come from the
// settings form (so they work before Save) and fall back to the saved ones;
// Origin is how the admin's browser reaches Lex, the first address offered.
type arrRequest struct {
	URL    string `json:"url"`
	Key    string `json:"key"`
	Origin string `json:"origin"`
}

func (s *Server) arrClient(w http.ResponseWriter, r *http.Request) (*arr.Client, arrRequest, bool) {
	var req arrRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request: "+err.Error())
		return nil, req, false
	}
	cfg := s.St.Config()
	var savedURL, savedKey string
	switch r.PathValue("app") {
	case "radarr":
		savedURL, savedKey = cfg.RadarrURL, cfg.RadarrKey
	case "sonarr":
		savedURL, savedKey = cfg.SonarrURL, cfg.SonarrKey
	default:
		writeErr(w, 404, "unknown app")
		return nil, req, false
	}
	if req.URL = strings.TrimSpace(req.URL); req.URL == "" {
		req.URL = savedURL
	}
	if req.Key = strings.TrimSpace(req.Key); req.Key == "" {
		req.Key = savedKey
	}
	if u, err := url.Parse(req.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, 400, "enter the address, like http://127.0.0.1:7878")
		return nil, req, false
	}
	if req.Key == "" {
		writeErr(w, 400, "enter the API key (Settings → General → API Key)")
		return nil, req, false
	}
	return &arr.Client{Base: req.URL, Key: req.Key}, req, true
}

func arrError(w http.ResponseWriter, err error) {
	code := 502
	if errors.Is(err, arr.ErrBadKey) {
		code = 400
	}
	writeErr(w, code, err.Error())
}

func (s *Server) webhookPath() string {
	return arr.ScanPath + "?token=" + url.QueryEscape(s.St.Config().WebhookToken)
}

// arrStatus reports whether the app already calls Lex's rescan webhook.
func (s *Server) arrStatus(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.arrClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	st, err := c.Check(ctx, s.webhookPath())
	if err != nil {
		arrError(w, err)
		return
	}
	writeJSON(w, st)
}

// arrConnect adds (or repairs) Lex's rescan webhook in the app, using the
// first Lex address the app can reach.
func (s *Server) arrConnect(w http.ResponseWriter, r *http.Request) {
	c, req, ok := s.arrClient(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	st, err := c.Connect(ctx, lexBases(req.Origin, s.ListenAddr, interfaceAddrs()), s.webhookPath())
	if err != nil {
		arrError(w, err)
		return
	}
	s.Log.Infof("%s webhook connected by %s: %s", st.App, userOf(r).Name, st.URL)
	writeJSON(w, st)
}

// lexBases lists the addresses an app might reach Lex at, best first: the
// one the admin's browser uses (a stable name, when it works from the app);
// the names Podman and Docker give a container for its host (apps in
// containers often can't resolve or route a Tailscale or proxy name, and
// these survive the host's LAN address changing); the server's LAN
// addresses; then loopback (an app on the same host network).
func lexBases(origin, listen string, ifaceAddrs []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(b string) {
		if b != "" && !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	if u, err := url.Parse(origin); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		add(u.Scheme + "://" + u.Host)
	}
	lan := lanBases(listen, ifaceAddrs)
	if host, port, err := net.SplitHostPort(listen); err == nil && len(lan) > 0 {
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			add("http://" + net.JoinHostPort("host.containers.internal", port))
			add("http://" + net.JoinHostPort("host.docker.internal", port))
		}
	}
	for _, b := range lan {
		add(b)
	}
	if host, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		if ip := net.ParseIP(host); host == "" || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			add("http://" + net.JoinHostPort("127.0.0.1", port))
		}
	}
	return out
}
