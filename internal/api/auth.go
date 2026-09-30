package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"lex/internal/store"
)

func (s *Server) publicInfo(w http.ResponseWriter, r *http.Request) {
	n, _ := s.St.UserCount()
	writeJSON(w, map[string]any{
		"serverName":    s.St.Config().ServerName,
		"version":       s.Version,
		"setupRequired": n == 0,
	})
}

func (s *Server) issueToken(w http.ResponseWriter, r *http.Request, u *store.User) {
	ip := s.clientIP(r)
	tok, err := s.St.CreateToken(u.ID, clientName(r.UserAgent()), ip)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "lex_token", Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.isHTTPS(r), Expires: time.Now().Add(365 * 24 * time.Hour),
	})
	writeJSON(w, map[string]any{"user": u, "token": tok})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	n, err := s.St.UserCount()
	if err != nil {
		writeErr(w, 503, "setup is temporarily unavailable")
		return
	}
	if n > 0 {
		writeErr(w, 409, store.ErrSetupCompleted.Error())
		return
	}
	var req struct{ Name, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u, err := s.St.CreateInitialAdmin(req.Name, req.Password)
	if errors.Is(err, store.ErrSetupCompleted) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Log.Infof("setup: created admin %q", u.Name)
	s.issueToken(w, r, u)
}

func (s *Server) loginAllowed(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFail[ip]
	if f == nil || time.Since(f.start) > 10*time.Minute {
		return true
	}
	return f.n < 10
}

func (s *Server) loginFailed(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFail[ip]
	if f == nil || time.Since(f.start) > 10*time.Minute {
		f = &failWindow{start: time.Now()}
		s.loginFail[ip] = f
	}
	f.n++
	// Keep the map bounded.
	if len(s.loginFail) > 1000 {
		for k, v := range s.loginFail {
			if time.Since(v.start) > 10*time.Minute {
				delete(s.loginFail, k)
			}
		}
		// Attackers can send many different IPs through a trusted proxy.
		// Evict the oldest window even if every entry is still live.
		if len(s.loginFail) > 1000 {
			var oldest string
			var started time.Time
			for k, v := range s.loginFail {
				if oldest == "" || v.start.Before(started) {
					oldest, started = k, v.start
				}
			}
			delete(s.loginFail, oldest)
		}
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.loginAllowed(ip) {
		writeErr(w, 429, "too many failed attempts; try again in a few minutes")
		return
	}
	var req struct{ Name, Password string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u, err := s.St.Authenticate(req.Name, req.Password)
	if err != nil {
		s.loginFailed(ip)
		s.Log.Warnf("failed login for %q from %s", req.Name, ip)
		writeErr(w, 401, "invalid username or password")
		return
	}
	s.Log.Infof("login: %s from %s", u.Name, ip)
	s.issueToken(w, r, u)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if t := s.token(r); t != "" {
		s.St.DeleteToken(t)
	}
	http.SetCookie(w, &http.Cookie{Name: "lex_token", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var prefs json.RawMessage = []byte(u.Prefs)
	if len(prefs) == 0 {
		prefs = []byte("{}")
	}
	writeJSON(w, map[string]any{"user": u, "prefs": prefs, "serverName": s.St.Config().ServerName, "version": s.Version})
}

func (s *Server) savePrefs(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := readJSON(r, &raw); err != nil || len(raw) > 64<<10 {
		writeErr(w, 400, "bad prefs")
		return
	}
	if err := s.St.SetPrefs(userOf(r).ID, string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var req struct{ Current, New string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u := userOf(r)
	if _, err := s.St.Authenticate(u.Name, req.Current); err != nil {
		writeErr(w, 403, "current password is incorrect")
		return
	}
	if err := s.St.SetPassword(u.ID, req.New); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// SetPassword revoked all tokens; sign this device back in.
	s.issueToken(w, r, u)
}

// webhookScan lets Sonarr/Radarr (Connect → Webhook) trigger a rescan.
func (s *Server) webhookScan(w http.ResponseWriter, r *http.Request) {
	want := s.St.Config().WebhookToken
	got := r.URL.Query().Get("token")
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		writeErr(w, 403, "bad token")
		return
	}
	s.Log.Infof("webhook: scan requested from %s", s.clientIP(r))
	s.Scanner.Trigger(0)
	writeJSON(w, map[string]bool{"ok": true})
}

// clientName makes a short "Browser on OS" label from a user agent.
func clientName(ua string) string {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	case strings.Contains(ua, "curl/"):
		browser = "curl"
	}
	osName := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		osName = "iPhone"
	case strings.Contains(ua, "iPad"):
		osName = "iPad"
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "Mac OS X"):
		osName = "macOS"
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "CrOS"):
		osName = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		osName = "Linux"
	}
	if strings.Contains(ua, "SMART-TV") || strings.Contains(ua, "Tizen") || strings.Contains(ua, "Web0S") {
		osName = "TV"
	}
	if osName == "" {
		return browser
	}
	return browser + " on " + osName
}
