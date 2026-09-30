// Package meta fetches metadata and artwork from online providers and local files.
package meta

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/store"
)

var ErrNoMatch = errors.New("no match")

type Candidate struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	Year     int    `json:"year"`
	Overview string `json:"overview"`
	Poster   string `json:"poster"`
	score    int
}

type Result struct {
	Provider      string
	ID            string
	Title         string
	OriginalTitle string
	Year          int
	Overview      string
	Tagline       string
	Rating        float64
	ContentRating string
	Genres        []string
	Studios       []string
	Cast          []store.Person
	Runtime       int
	Premiere      string
	PosterURL     string
	BackdropURL   string
	IDs           map[string]string
}

type EpisodeMeta struct {
	Season   int
	Episode  int
	Title    string
	Overview string
	Premiere string
	StillURL string
	Rating   float64
	Runtime  int
}

type SeasonMeta struct {
	Season    int
	Title     string
	Overview  string
	PosterURL string
	Premiere  string
	Episodes  []EpisodeMeta
}

// limiter enforces a minimum interval between requests to a host.
type limiter struct {
	mu   sync.Mutex
	last time.Time
	gap  time.Duration
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	next := l.last.Add(l.gap)
	now := time.Now()
	if next.Before(now) {
		next = now
	}
	l.last = next
	l.mu.Unlock()
	d := time.Until(next)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var httpClient = &http.Client{Timeout: 25 * time.Second}

// Artwork URLs come from metadata providers. Validate resolved addresses at
// connection time so redirects and DNS rebinding cannot reach local services.
var artworkClient = &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{
	DialContext:         dialArtwork,
	TLSHandshakeTimeout: 10 * time.Second,
	IdleConnTimeout:     90 * time.Second,
}}

func publicArtworkIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func dialArtwork(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, errors.New("artwork host lookup failed")
	}
	if len(ips) == 0 {
		return nil, errors.New("artwork host has no addresses")
	}
	for _, ip := range ips {
		if !publicArtworkIP(ip.IP) {
			return nil, errors.New("artwork URL must use a public address")
		}
	}
	var lastErr error
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

const userAgent = "Lex/1.0 (+self-hosted media server)"

// getJSON fetches url into v, retrying on 429/5xx.
func getJSON(ctx context.Context, lim *limiter, url string, headers map[string]string, v any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if lim != nil {
			if err := lim.wait(ctx); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Accept", "application/json")
		for k, h := range headers {
			req.Header.Set(k, h)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: metadata request failed", redact(url))
			sleep(ctx, time.Duration(attempt+1)*time.Second)
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			wait := time.Duration(attempt+1) * 2 * time.Second
			if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 && ra < 60 {
				wait = time.Duration(ra) * time.Second
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: HTTP %d", redact(url), resp.StatusCode)
			sleep(ctx, wait)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode == 404 {
			return ErrNoMatch
		}
		if resp.StatusCode != 200 {
			return fmt.Errorf("%s: HTTP %d", redact(url), resp.StatusCode)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
	}
	return lastErr
}

func redact(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "[invalid URL]"
	}
	parsed.User = nil
	parsed.RawQuery, parsed.Fragment = "", ""
	return parsed.String()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// ImageCache downloads remote artwork once and serves it from disk.
type ImageCache struct {
	Dir string
	mu  sync.Mutex
}

func NewImageCache(dir string) *ImageCache {
	os.MkdirAll(dir, 0o755)
	return &ImageCache{Dir: dir}
}

// Fetch downloads url (if not cached) and returns a "cache:<name>" reference.
func (c *ImageCache) Fetch(ctx context.Context, url string) (string, error) {
	if url == "" {
		return "", nil
	}
	h := sha1.Sum([]byte(url))
	ext := strings.ToLower(filepath.Ext(strings.SplitN(url, "?", 2)[0]))
	if ext != ".png" && ext != ".webp" {
		ext = ".jpg"
	}
	name := hex.EncodeToString(h[:10]) + ext
	dst := filepath.Join(c.Dir, name)
	if _, err := os.Stat(dst); err == nil {
		return "cache:" + name, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	if req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
		return "", errors.New("invalid artwork URL")
	}
	resp, err := artworkClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("image %s: download failed", redact(url))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("image %s: HTTP %d", redact(url), resp.StatusCode)
	}
	tmp, err := os.CreateTemp(c.Dir, ".dl-*")
	if err != nil {
		return "", err
	}
	if n, err := io.Copy(tmp, io.LimitReader(resp.Body, (20<<20)+1)); err != nil || n > 20<<20 {
		tmp.Close()
		os.Remove(tmp.Name())
		if err == nil {
			err = errors.New("artwork exceeds 20 MiB")
		}
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return "cache:" + name, nil
}

// Path resolves an image reference to a file on disk.
func (c *ImageCache) Path(ref string) string {
	switch {
	case strings.HasPrefix(ref, "cache:"):
		name := filepath.Base(strings.TrimPrefix(ref, "cache:"))
		return filepath.Join(c.Dir, name)
	case strings.HasPrefix(ref, "file:"):
		return strings.TrimPrefix(ref, "file:")
	}
	return ""
}

// ThumbPath is where generated video thumbnails for an item go.
func (c *ImageCache) ThumbPath(itemID int64) string {
	return filepath.Join(c.Dir, fmt.Sprintf("thumb-%d.jpg", itemID))
}

func yearOf(date string) int {
	if len(date) >= 4 {
		y, _ := strconv.Atoi(date[:4])
		return y
	}
	return 0
}

func stripHTML(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&nbsp;", " ").Replace(b.String()))
}
