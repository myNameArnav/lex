package meta

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/store"
)

// Radarr uses a local Radarr instance as a keyless TMDB source: its library
// holds full metadata for managed movies and its lookup endpoints proxy TMDB
// searches for everything else.
type Radarr struct {
	URL string
	Key string
	lim limiter

	mu     sync.Mutex
	lib    []radarrMovie
	libAt  time.Time
	libURL string
}

func NewRadarr() *Radarr { return &Radarr{lim: limiter{gap: 100 * time.Millisecond}} }

type radarrImage struct {
	CoverType string `json:"coverType"`
	RemoteURL string `json:"remoteUrl"`
	URL       string `json:"url"`
}

type radarrMovie struct {
	ID            int           `json:"id"`
	Title         string        `json:"title"`
	OriginalTitle string        `json:"originalTitle"`
	Year          int           `json:"year"`
	TMDBID        int           `json:"tmdbId"`
	IMDBID        string        `json:"imdbId"`
	Overview      string        `json:"overview"`
	Runtime       int           `json:"runtime"`
	Certification string        `json:"certification"`
	Genres        []string      `json:"genres"`
	Studio        string        `json:"studio"`
	InCinemas     string        `json:"inCinemas"`
	DigitalRel    string        `json:"digitalRelease"`
	Path          string        `json:"path"`
	FolderName    string        `json:"folderName"`
	Images        []radarrImage `json:"images"`
	Ratings       map[string]struct {
		Value float64 `json:"value"`
	} `json:"ratings"`
}

// SetEndpoint updates the server address/key (safe for concurrent use).
func (r *Radarr) SetEndpoint(u, key string) {
	r.mu.Lock()
	r.URL, r.Key = u, key
	r.mu.Unlock()
}

func (r *Radarr) endpoint() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.URL, r.Key
}

func (r *Radarr) get(ctx context.Context, p string, q url.Values, v any) error {
	base, key := r.endpoint()
	u := strings.TrimRight(base, "/") + "/api/v3" + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return getJSON(ctx, &r.lim, u, map[string]string{"X-Api-Key": key}, v)
}

// library returns Radarr's movie list, cached for a few minutes.
func (r *Radarr) library(ctx context.Context) ([]radarrMovie, error) {
	base, _ := r.endpoint()
	r.mu.Lock()
	if r.lib != nil && r.libURL == base && time.Since(r.libAt) < 5*time.Minute {
		defer r.mu.Unlock()
		return r.lib, nil
	}
	r.mu.Unlock()
	var lib []radarrMovie
	if err := r.get(ctx, "/movie", nil, &lib); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.lib, r.libAt, r.libURL = lib, time.Now(), base
	r.mu.Unlock()
	return lib, nil
}

// MatchFolder finds a library movie by its folder name (Radarr sees the
// media through its own mount, so compare basenames, not full paths).
func (r *Radarr) MatchFolder(ctx context.Context, folder string) string {
	if folder == "" {
		return ""
	}
	lib, err := r.library(ctx)
	if err != nil {
		return ""
	}
	want := strings.ToLower(path.Base(strings.ReplaceAll(folder, "\\", "/")))
	for _, m := range lib {
		if strings.ToLower(path.Base(m.Path)) == want && m.TMDBID > 0 {
			return strconv.Itoa(m.TMDBID)
		}
	}
	return ""
}

func (r *Radarr) FindIMDB(ctx context.Context, imdb string) string {
	var m radarrMovie
	if err := r.get(ctx, "/movie/lookup/imdb", url.Values{"imdbId": {imdb}}, &m); err != nil || m.TMDBID == 0 {
		return ""
	}
	return strconv.Itoa(m.TMDBID)
}

func (r *Radarr) Search(ctx context.Context, title string, year int) ([]Candidate, error) {
	term := title
	if year > 0 {
		term += " " + strconv.Itoa(year)
	}
	var res []radarrMovie
	if err := r.get(ctx, "/movie/lookup", url.Values{"term": {term}}, &res); err != nil {
		return nil, err
	}
	// Radarr's lookup is fuzzy; retry without the year if it found nothing.
	if len(res) == 0 && year > 0 {
		if err := r.get(ctx, "/movie/lookup", url.Values{"term": {title}}, &res); err != nil {
			return nil, err
		}
	}
	var out []Candidate
	for _, m := range res {
		if m.TMDBID == 0 {
			continue
		}
		out = append(out, Candidate{Provider: "radarr", ID: strconv.Itoa(m.TMDBID), Kind: "movie", Title: m.Title, Year: m.Year, Overview: m.Overview, Poster: tmdbSized(m.image("poster"), "w185")})
	}
	return out, nil
}

func (m *radarrMovie) image(kind string) string {
	for _, im := range m.Images {
		if im.CoverType == kind && im.RemoteURL != "" {
			return im.RemoteURL
		}
	}
	return ""
}

// tmdbSized swaps TMDB's "original" size for a smaller rendition.
func tmdbSized(u, size string) string {
	return strings.Replace(u, "/t/p/original/", "/t/p/"+size+"/", 1)
}

func (r *Radarr) Movie(ctx context.Context, tmdbID string) (*Result, error) {
	id, _ := strconv.Atoi(tmdbID)
	var m *radarrMovie
	if lib, err := r.library(ctx); err == nil {
		for i := range lib {
			if lib[i].TMDBID == id {
				m = &lib[i]
				break
			}
		}
	}
	if m == nil {
		var lm radarrMovie
		if err := r.get(ctx, "/movie/lookup/tmdb", url.Values{"tmdbId": {tmdbID}}, &lm); err != nil {
			return nil, err
		}
		if lm.TMDBID == 0 {
			return nil, ErrNoMatch
		}
		m = &lm
	}
	res := &Result{
		Provider: "radarr", ID: strconv.Itoa(m.TMDBID), Title: m.Title, Year: m.Year, Overview: m.Overview,
		Runtime: m.Runtime, ContentRating: m.Certification, Genres: m.Genres,
		PosterURL: tmdbSized(m.image("poster"), "w500"), BackdropURL: tmdbSized(m.image("fanart"), "w1280"),
		IDs: map[string]string{"tmdb": strconv.Itoa(m.TMDBID)},
	}
	if m.OriginalTitle != m.Title {
		res.OriginalTitle = m.OriginalTitle
	}
	if m.IMDBID != "" {
		res.IDs["imdb"] = m.IMDBID
	}
	if m.Studio != "" {
		res.Studios = []string{m.Studio}
	}
	for _, k := range []string{"tmdb", "imdb", "trakt"} {
		if v, ok := m.Ratings[k]; ok && v.Value > 0 {
			res.Rating = v.Value
			if k == "trakt" {
				res.Rating = float64(int(v.Value*10+0.5)) / 10
			}
			break
		}
	}
	for _, d := range []string{m.InCinemas, m.DigitalRel} {
		if len(d) >= 10 {
			res.Premiere = d[:10]
			break
		}
	}
	if m.ID > 0 {
		res.Cast = r.credits(ctx, m.ID)
	}
	return res, nil
}

func (r *Radarr) credits(ctx context.Context, movieID int) []store.Person {
	var cr []struct {
		PersonName string        `json:"personName"`
		Character  string        `json:"character"`
		Type       string        `json:"type"`
		Job        string        `json:"job"`
		Order      int           `json:"order"`
		Images     []radarrImage `json:"images"`
	}
	if err := r.get(ctx, "/credit", url.Values{"movieId": {strconv.Itoa(movieID)}}, &cr); err != nil {
		return nil
	}
	var out []store.Person
	for _, c := range cr {
		if c.Type == "crew" && c.Job == "Director" && len(out) < 2 {
			out = append(out, store.Person{Name: c.PersonName, Role: "Director"})
		}
	}
	sort.SliceStable(cr, func(i, j int) bool { return cr[i].Order < cr[j].Order })
	n := 0
	for _, c := range cr {
		if c.Type != "cast" || n >= 20 {
			continue
		}
		p := store.Person{Name: c.PersonName, Role: c.Character}
		for _, im := range c.Images {
			if im.CoverType == "headshot" && im.RemoteURL != "" {
				p.Image = tmdbSized(im.RemoteURL, "w185")
			}
		}
		out = append(out, p)
		n++
	}
	return out
}

// folderOf returns the folder name used to match an item against Radarr.
func folderOf(it *store.Item, filePath string) string {
	// File-named movies (several per folder) have synthetic "dir#title" keys.
	if strings.Contains(it.Path, "#") || filePath == "" {
		return ""
	}
	return path.Base(it.Path)
}

// DetectRadarr looks for a local Radarr install and returns its API URL and
// key, verified with a status request. Empty strings if none is found.
func DetectRadarr(ctx context.Context) (string, string) {
	home, _ := os.UserHomeDir()
	cands := []string{
		"/opt/podman/radarr/config/config.xml", "/opt/docker/radarr/config/config.xml", "/opt/radarr/config/config.xml",
		"/var/lib/radarr/config.xml", "/mnt/dietpi_userdata/radarr/config.xml", "/config/radarr/config.xml",
		filepath.Join(home, ".config/Radarr/config.xml"),
	}
	for _, p := range cands {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var c struct {
			Port    int    `xml:"Port"`
			APIKey  string `xml:"ApiKey"`
			URLBase string `xml:"UrlBase"`
		}
		if xml.Unmarshal(b, &c) != nil || c.APIKey == "" {
			continue
		}
		if c.Port == 0 {
			c.Port = 7878
		}
		u := fmt.Sprintf("http://127.0.0.1:%d%s", c.Port, strings.TrimRight(c.URLBase, "/"))
		r := NewRadarr()
		r.SetEndpoint(u, c.APIKey)
		var st struct {
			Version string `json:"version"`
		}
		if err := r.get(ctx, "/system/status", nil, &st); err == nil && st.Version != "" {
			return u, c.APIKey
		}
	}
	return "", ""
}
