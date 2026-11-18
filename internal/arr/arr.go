// Package arr sets up Lex's rescan webhook in Sonarr or Radarr through their
// API, so the admin doesn't have to copy URLs between apps.
//
// It only relies on the notification API every Servarr app (Sonarr, Radarr,
// Lidarr, ...) has shared since v3, and it builds the connection from the
// app's own Webhook template (GET /notification/schema) rather than a
// hard-coded body. Fields and event flags Lex doesn't know about keep the
// app's defaults, so new app versions that add or rename events still work.
package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Name is what the connection is called in the app's Connect list.
const Name = "Lex"

// Client talks to one Sonarr/Radarr instance.
type Client struct {
	Base string // e.g. http://127.0.0.1:7878 (with any URL base)
	Key  string
	HTTP *http.Client
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+"/api/v3"+path, rd)
	if err != nil {
		return fmt.Errorf("bad address: %w", err)
	}
	req.Header.Set("X-Api-Key", c.Key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnreachable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ErrBadKey
	case resp.StatusCode >= 300:
		return &Error{Status: resp.StatusCode, Msg: failureMessage(data)}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("unexpected reply (is this Sonarr or Radarr?)")
		}
	}
	return nil
}

var (
	ErrUnreachable = errors.New("can't reach it at that address")
	ErrBadKey      = errors.New("the API key was rejected")
	ErrNoWebhook   = errors.New("this version has no Webhook connection type")
)

// Error is a failed API call; Msg is the app's own explanation when it gave one.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// failureMessage pulls the text out of a Servarr validation failure list
// ([{"propertyName":..., "errorMessage":...}]) or error object.
func failureMessage(b []byte) string {
	var list []struct {
		ErrorMessage string `json:"errorMessage"`
	}
	if json.Unmarshal(b, &list) == nil {
		var msgs []string
		for _, f := range list {
			if f.ErrorMessage != "" {
				msgs = append(msgs, f.ErrorMessage)
			}
		}
		return strings.Join(msgs, "; ")
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &obj) == nil {
		return obj.Message
	}
	return ""
}

// notification is kept as a raw map so fields Lex doesn't know survive a
// round trip unchanged.
type notification map[string]any

func (n notification) field(name string) map[string]any {
	fs, _ := n["fields"].([]any)
	for _, f := range fs {
		if m, ok := f.(map[string]any); ok && m["name"] == name {
			return m
		}
	}
	return nil
}

func (n notification) url() string {
	if f := n.field("url"); f != nil {
		s, _ := f["value"].(string)
		return s
	}
	return ""
}

// ScanPath is Lex's webhook endpoint; the full URL adds ?token=.
const ScanPath = "/api/webhook/scan"

// isLex reports whether n is a webhook pointing at a Lex scan endpoint
// (with any token, so a stale one is found and fixed rather than duplicated).
func (n notification) isLex() bool {
	impl, _ := n["implementation"].(string)
	return strings.EqualFold(impl, "Webhook") && strings.Contains(n.url(), ScanPath)
}

// libraryEvents are the events that change files on disk: imports,
// upgrades, renames and deletions. Matched by substring so that variants
// (onMovieFileDelete, onEpisodeFileDeleteForUpgrade, onImportComplete)
// and future events of the same kinds are picked up.
var libraryEvents = []string{"Download", "Upgrade", "Import", "Rename", "Delete"}

// prepare points n at url and turns on every library-changing event the app
// says this connection type supports.
func (n notification) prepare(url string) error {
	f := n.field("url")
	if f == nil {
		return ErrNoWebhook
	}
	f["value"] = url
	for k := range n {
		if !strings.HasPrefix(k, "on") || len(k) < 3 || !unicode.IsUpper(rune(k[2])) {
			continue
		}
		if sup, _ := n["supports"+strings.ToUpper(k[:1])+k[1:]].(bool); !sup {
			continue
		}
		for _, ev := range libraryEvents {
			if strings.Contains(k, ev) {
				n[k] = true
				break
			}
		}
	}
	return nil
}

// Status is what the app has for Lex.
type Status struct {
	App       string `json:"app"`     // e.g. "Radarr", from the app itself
	Version   string `json:"version"` // app version
	Connected bool   `json:"connected"`
	URL       string `json:"url,omitempty"` // the Lex address the app calls (token stripped)
}

func (c *Client) app(ctx context.Context) (string, string, error) {
	var st struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := c.do(ctx, "GET", "/system/status", nil, &st); err != nil {
		return "", "", err
	}
	return st.AppName, st.Version, nil
}

// Check reports whether the app already calls Lex's webhook at path.
func (c *Client) Check(ctx context.Context, path string) (Status, error) {
	var s Status
	var err error
	if s.App, s.Version, err = c.app(ctx); err != nil {
		return s, err
	}
	var list []notification
	if err := c.do(ctx, "GET", "/notification", nil, &list); err != nil {
		return s, err
	}
	for _, n := range list {
		// A webhook with an old token counts as not connected: it would be refused.
		if n.isLex() && strings.HasSuffix(n.url(), path) {
			s.Connected, s.URL = true, stripQuery(n.url())
			break
		}
	}
	return s, nil
}

// Connect makes the app call target (a Lex base URL + path) on library
// changes. Each base in bases is tried in order with the app's own Test, so
// the first address the app can actually reach wins: the app often runs in
// a container that can't see the name the admin's browser uses. An existing
// Lex webhook is updated in place rather than duplicated.
func (c *Client) Connect(ctx context.Context, bases []string, path string) (Status, error) {
	var s Status
	var err error
	if s.App, s.Version, err = c.app(ctx); err != nil {
		return s, err
	}
	var schema []notification
	if err := c.do(ctx, "GET", "/notification/schema", nil, &schema); err != nil {
		return s, err
	}
	var tmpl notification
	for _, n := range schema {
		if impl, _ := n["implementation"].(string); strings.EqualFold(impl, "Webhook") {
			tmpl = n
			break
		}
	}
	if tmpl == nil {
		return s, ErrNoWebhook
	}
	var list []notification
	if err := c.do(ctx, "GET", "/notification", nil, &list); err != nil {
		return s, err
	}
	n := tmpl
	n["name"] = Name
	for _, ex := range list {
		if ex.isLex() {
			n = ex
			break
		}
	}

	var lastErr error
	for _, base := range bases {
		u := strings.TrimRight(base, "/") + path
		if err := n.prepare(u); err != nil {
			return s, err
		}
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := c.do(tctx, "POST", "/notification/test", n, nil)
		cancel()
		if err != nil {
			if errors.Is(err, ErrBadKey) || ctx.Err() != nil {
				return s, err
			}
			lastErr = fmt.Errorf("%s couldn't reach lex at %s: %w", s.App, base, err)
			continue
		}
		if id, ok := n["id"].(float64); ok && id > 0 {
			err = c.do(ctx, "PUT", fmt.Sprintf("/notification/%d?forceSave=true", int(id)), n, nil)
		} else {
			delete(n, "id")
			err = c.do(ctx, "POST", "/notification?forceSave=true", n, nil)
		}
		if err != nil {
			return s, err
		}
		s.Connected, s.URL = true, base+stripQuery(path)
		return s, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no address for lex to offer")
	}
	return s, lastErr
}

func stripQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}
