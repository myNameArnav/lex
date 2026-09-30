// Package subsearch finds and downloads subtitles from OpenSubtitles.com.
package subsearch

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiBase = "https://api.opensubtitles.com/api/v1"
const userAgent = "Lex v1.0"

var ErrNoKey = errors.New("add an OpenSubtitles API key in Settings → Metadata to search for subtitles")

type Client struct {
	Key, User, Pass string

	mu        sync.Mutex
	token     string
	tokenBase string
	tokenFor  string
	tokenAt   time.Time
}

var http_ = &http.Client{Timeout: 30 * time.Second}

type Query struct {
	IMDB       string // tt-prefixed or numeric
	TMDB       string
	ParentIMDB string
	ParentTMDB string
	Season     int
	Episode    int
	Title      string
	Year       int
	Language   string // ISO 639-1 / 639-2B codes, comma separated
	Hash       string
}

type Result struct {
	FileID          int64   `json:"fileId"`
	FileName        string  `json:"fileName"`
	Release         string  `json:"release"`
	Language        string  `json:"language"`
	Downloads       int     `json:"downloads"`
	Rating          float64 `json:"rating"`
	HearingImpaired bool    `json:"hearingImpaired"`
	Trusted         bool    `json:"trusted"`
	HashMatch       bool    `json:"hashMatch"`
	AI              bool    `json:"ai"`
	Title           string  `json:"title"`
}

func (c *Client) do(ctx context.Context, method, base, path string, q url.Values, body any, v any) error {
	if c.Key == "" {
		return ErrNoKey
	}
	u := base + path
	if len(q) > 0 {
		// The API redirects unless parameters are sorted and lower-case.
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(strings.ToLower(q.Get(k))))
		}
		u += "?" + strings.Join(parts, "&")
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Api-Key", c.Key)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := c.currentToken(); tok != "" && path == "/download" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http_.Do(req)
	if err != nil {
		return fmt.Errorf("opensubtitles: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Message string   `json:"message"`
			Errors  []string `json:"errors"`
		}
		json.Unmarshal(data, &e)
		msg := e.Message
		if msg == "" && len(e.Errors) > 0 {
			msg = strings.Join(e.Errors, "; ")
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("opensubtitles: %s (HTTP %d)", msg, resp.StatusCode)
	}
	return json.Unmarshal(data, v)
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenFor != c.User+"\x00"+c.Pass || time.Since(c.tokenAt) > 12*time.Hour {
		return ""
	}
	return c.token
}

// login gets a user token (more downloads per day than anonymous use).
func (c *Client) login(ctx context.Context) {
	if c.User == "" || c.Pass == "" || c.currentToken() != "" {
		return
	}
	var r struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	if err := c.do(ctx, "POST", apiBase, "/login", nil, map[string]string{"username": c.User, "password": c.Pass}, &r); err != nil || r.Token == "" {
		return
	}
	c.mu.Lock()
	c.token, c.tokenAt, c.tokenFor = r.Token, time.Now(), c.User+"\x00"+c.Pass
	c.tokenBase = ""
	if r.BaseURL != "" {
		c.tokenBase = "https://" + strings.TrimPrefix(strings.TrimPrefix(r.BaseURL, "https://"), "http://") + "/api/v1"
	}
	c.mu.Unlock()
}

func numericID(s string) string {
	return strings.TrimLeft(strings.TrimPrefix(strings.ToLower(s), "tt"), "0")
}

func (c *Client) Search(ctx context.Context, q Query) ([]Result, error) {
	v := url.Values{}
	if q.Language != "" {
		v.Set("languages", q.Language)
	}
	if q.Hash != "" {
		v.Set("moviehash", q.Hash)
	}
	switch {
	case q.ParentIMDB != "" || q.ParentTMDB != "":
		if q.ParentIMDB != "" {
			v.Set("parent_imdb_id", numericID(q.ParentIMDB))
		} else {
			v.Set("parent_tmdb_id", q.ParentTMDB)
		}
		v.Set("season_number", strconv.Itoa(q.Season))
		v.Set("episode_number", strconv.Itoa(q.Episode))
	case q.IMDB != "":
		v.Set("imdb_id", numericID(q.IMDB))
	case q.TMDB != "":
		v.Set("tmdb_id", q.TMDB)
	default:
		v.Set("query", q.Title)
		if q.Year > 0 {
			v.Set("year", strconv.Itoa(q.Year))
		}
		if q.Season > 0 {
			v.Set("season_number", strconv.Itoa(q.Season))
			v.Set("episode_number", strconv.Itoa(q.Episode))
		}
	}
	var r struct {
		Data []struct {
			Attributes struct {
				Language        string  `json:"language"`
				DownloadCount   int     `json:"download_count"`
				HearingImpaired bool    `json:"hearing_impaired"`
				Ratings         float64 `json:"ratings"`
				FromTrusted     bool    `json:"from_trusted"`
				MoviehashMatch  bool    `json:"moviehash_match"`
				AITranslated    bool    `json:"ai_translated"`
				MachineTrans    bool    `json:"machine_translated"`
				Release         string  `json:"release"`
				Files           []struct {
					FileID   int64  `json:"file_id"`
					FileName string `json:"file_name"`
				} `json:"files"`
				Feature struct {
					Title string `json:"title"`
					Year  int    `json:"year"`
				} `json:"feature_details"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := c.do(ctx, "GET", apiBase, "/subtitles", v, nil, &r); err != nil {
		return nil, err
	}
	out := []Result{}
	for _, d := range r.Data {
		a := d.Attributes
		if len(a.Files) == 0 {
			continue
		}
		out = append(out, Result{
			FileID: a.Files[0].FileID, FileName: a.Files[0].FileName, Release: a.Release, Language: a.Language,
			Downloads: a.DownloadCount, Rating: a.Ratings, HearingImpaired: a.HearingImpaired, Trusted: a.FromTrusted,
			HashMatch: a.MoviehashMatch, AI: a.AITranslated || a.MachineTrans, Title: a.Feature.Title,
		})
	}
	// Exact-release matches first, then trusted, then popularity.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].HashMatch != out[j].HashMatch {
			return out[i].HashMatch
		}
		if out[i].AI != out[j].AI {
			return !out[i].AI
		}
		return out[i].Downloads > out[j].Downloads
	})
	return out, nil
}

// Download fetches a subtitle file (converted to SRT by the API).
func (c *Client) Download(ctx context.Context, fileID int64) ([]byte, string, error) {
	c.login(ctx)
	base := apiBase
	c.mu.Lock()
	if c.tokenBase != "" && c.token != "" {
		base = c.tokenBase
	}
	c.mu.Unlock()
	var r struct {
		Link      string `json:"link"`
		FileName  string `json:"file_name"`
		Remaining int    `json:"remaining"`
		Message   string `json:"message"`
	}
	if err := c.do(ctx, "POST", base, "/download", nil, map[string]any{"file_id": fileID, "sub_format": "srt"}, &r); err != nil {
		return nil, "", err
	}
	if r.Link == "" {
		return nil, "", fmt.Errorf("opensubtitles: %s", r.Message)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", r.Link, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http_.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("opensubtitles download: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return data, r.FileName, err
}

// Hash computes the OpenSubtitles "moviehash": file size plus the 64-bit
// sums of the first and last 64 KiB, which identifies an exact release.
func Hash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	const chunk = 65536
	size := st.Size()
	if size < chunk*2 {
		return "", errors.New("file too small to hash")
	}
	sum := uint64(size)
	buf := make([]byte, chunk)
	for _, off := range []int64{0, size - chunk} {
		if _, err := f.ReadAt(buf, off); err != nil {
			return "", err
		}
		for i := 0; i < chunk; i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i:])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}
