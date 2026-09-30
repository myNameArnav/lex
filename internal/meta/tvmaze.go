package meta

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"lex/internal/store"
)

const tvmazeBase = "https://api.tvmaze.com"

// TVmaze needs no API key. Its limit is ~20 calls / 10 s per IP.
type TVmaze struct {
	lim limiter
	// Episode lists are fetched once per show per agent run.
	mu    sync.Mutex
	cache map[string][]tvmazeEpisode
}

func NewTVmaze() *TVmaze {
	return &TVmaze{lim: limiter{gap: 550 * time.Millisecond}, cache: map[string][]tvmazeEpisode{}}
}

type tvmazeImage struct {
	Medium   string `json:"medium"`
	Original string `json:"original"`
}

type tvmazeShow struct {
	ID        int                       `json:"id"`
	Name      string                    `json:"name"`
	Language  string                    `json:"language"`
	Genres    []string                  `json:"genres"`
	Runtime   int                       `json:"runtime"`
	AvgRun    int                       `json:"averageRuntime"`
	Premiered string                    `json:"premiered"`
	Summary   string                    `json:"summary"`
	Rating    struct{ Average float64 } `json:"rating"`
	Network   *named                    `json:"network"`
	WebChan   *named                    `json:"webChannel"`
	Image     *tvmazeImage              `json:"image"`
	Externals struct {
		TVDB int    `json:"thetvdb"`
		IMDB string `json:"imdb"`
	} `json:"externals"`
	Embedded struct {
		Cast []struct {
			Person struct {
				Name  string       `json:"name"`
				Image *tvmazeImage `json:"image"`
			} `json:"person"`
			Character struct {
				Name string `json:"name"`
			} `json:"character"`
		} `json:"cast"`
		Images []struct {
			Type        string `json:"type"`
			Main        bool   `json:"main"`
			Resolutions struct {
				Original struct {
					URL   string `json:"url"`
					Width int    `json:"width"`
				} `json:"original"`
			} `json:"resolutions"`
		} `json:"images"`
	} `json:"_embedded"`
}

type tvmazeEpisode struct {
	Season  int                       `json:"season"`
	Number  *int                      `json:"number"`
	Name    string                    `json:"name"`
	Airdate string                    `json:"airdate"`
	Runtime int                       `json:"runtime"`
	Summary string                    `json:"summary"`
	Type    string                    `json:"type"`
	Rating  struct{ Average float64 } `json:"rating"`
	Image   *tvmazeImage              `json:"image"`
}

func (t *TVmaze) Search(ctx context.Context, title string, year int) ([]Candidate, error) {
	var r []struct {
		Score float64    `json:"score"`
		Show  tvmazeShow `json:"show"`
	}
	if err := getJSON(ctx, &t.lim, tvmazeBase+"/search/shows?q="+url.QueryEscape(title), nil, &r); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, x := range r {
		c := Candidate{Provider: "tvmaze", ID: strconv.Itoa(x.Show.ID), Kind: "show", Title: x.Show.Name, Year: yearOf(x.Show.Premiered), Overview: stripHTML(x.Show.Summary)}
		if x.Show.Image != nil {
			c.Poster = x.Show.Image.Medium
		}
		out = append(out, c)
	}
	return out, nil
}

// Lookup resolves a TVDB or IMDb id to a TVmaze id.
func (t *TVmaze) Lookup(ctx context.Context, source, id string) (string, error) {
	key := map[string]string{"tvdb": "thetvdb", "imdb": "imdb"}[source]
	if key == "" {
		return "", ErrNoMatch
	}
	var s tvmazeShow
	if err := getJSON(ctx, &t.lim, tvmazeBase+"/lookup/shows?"+key+"="+url.QueryEscape(id), nil, &s); err != nil {
		return "", err
	}
	if s.ID == 0 {
		return "", ErrNoMatch
	}
	return strconv.Itoa(s.ID), nil
}

func (t *TVmaze) Show(ctx context.Context, id string) (*Result, error) {
	var s tvmazeShow
	if err := getJSON(ctx, &t.lim, tvmazeBase+"/shows/"+url.PathEscape(id)+"?embed[]=cast&embed[]=images", nil, &s); err != nil {
		return nil, err
	}
	r := &Result{
		Provider: "tvmaze", ID: strconv.Itoa(s.ID), Title: s.Name, Year: yearOf(s.Premiered), Premiere: s.Premiered,
		Overview: stripHTML(s.Summary), Rating: s.Rating.Average, Genres: s.Genres, Runtime: s.AvgRun,
		IDs: map[string]string{"tvmaze": strconv.Itoa(s.ID)},
	}
	if r.Runtime == 0 {
		r.Runtime = s.Runtime
	}
	if s.Network != nil {
		r.Studios = []string{s.Network.Name}
	} else if s.WebChan != nil {
		r.Studios = []string{s.WebChan.Name}
	}
	if s.Image != nil {
		r.PosterURL = s.Image.Original
	}
	if s.Externals.TVDB > 0 {
		r.IDs["tvdb"] = strconv.Itoa(s.Externals.TVDB)
	}
	if s.Externals.IMDB != "" {
		r.IDs["imdb"] = s.Externals.IMDB
	}
	// Prefer the main background image, then the widest.
	bestW := -1
	for _, im := range s.Embedded.Images {
		if im.Type != "background" {
			continue
		}
		w := im.Resolutions.Original.Width
		if im.Main {
			w += 100000
		}
		if w > bestW {
			bestW = w
			r.BackdropURL = im.Resolutions.Original.URL
		}
	}
	for i, c := range s.Embedded.Cast {
		if i >= 20 {
			break
		}
		p := store.Person{Name: c.Person.Name, Role: c.Character.Name}
		if c.Person.Image != nil {
			p.Image = c.Person.Image.Medium
		}
		r.Cast = append(r.Cast, p)
	}
	return r, nil
}

func (t *TVmaze) episodes(ctx context.Context, id string) ([]tvmazeEpisode, error) {
	t.mu.Lock()
	if e, ok := t.cache[id]; ok {
		t.mu.Unlock()
		return e, nil
	}
	t.mu.Unlock()
	var eps []tvmazeEpisode
	if err := getJSON(ctx, &t.lim, tvmazeBase+"/shows/"+url.PathEscape(id)+"/episodes?specials=1", nil, &eps); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.cache[id] = eps
	t.mu.Unlock()
	return eps, nil
}

func (t *TVmaze) ResetCache() {
	t.mu.Lock()
	t.cache = map[string][]tvmazeEpisode{}
	t.mu.Unlock()
}

func (t *TVmaze) Season(ctx context.Context, id string, season int) (*SeasonMeta, error) {
	eps, err := t.episodes(ctx, id)
	if err != nil {
		return nil, err
	}
	sm := &SeasonMeta{Season: season, Title: fmt.Sprintf("Season %d", season)}
	if season == 0 {
		sm.Title = "Specials"
	}
	// TVmaze specials have no number; number them in air order.
	var specials []tvmazeEpisode
	for _, e := range eps {
		if e.Number == nil || e.Type == "significant_special" || e.Type == "insignificant_special" {
			specials = append(specials, e)
			continue
		}
		if e.Season != season {
			continue
		}
		sm.Episodes = append(sm.Episodes, tvEp(e, season, *e.Number))
	}
	if season == 0 {
		sort.SliceStable(specials, func(i, j int) bool { return specials[i].Airdate < specials[j].Airdate })
		for i, e := range specials {
			sm.Episodes = append(sm.Episodes, tvEp(e, 0, i+1))
		}
	}
	if len(sm.Episodes) > 0 {
		sm.Premiere = sm.Episodes[0].Premiere
	}
	// Season posters come from a separate endpoint.
	var seasons []struct {
		Number  int          `json:"number"`
		Name    string       `json:"name"`
		Summary string       `json:"summary"`
		Image   *tvmazeImage `json:"image"`
	}
	if season > 0 && getJSON(ctx, &t.lim, tvmazeBase+"/shows/"+url.PathEscape(id)+"/seasons", nil, &seasons) == nil {
		for _, s := range seasons {
			if s.Number == season {
				if s.Image != nil {
					sm.PosterURL = s.Image.Original
				}
				if s.Name != "" {
					sm.Title = s.Name
				}
				sm.Overview = stripHTML(s.Summary)
			}
		}
	}
	return sm, nil
}

func tvEp(e tvmazeEpisode, season, num int) EpisodeMeta {
	em := EpisodeMeta{Season: season, Episode: num, Title: e.Name, Overview: stripHTML(e.Summary), Premiere: e.Airdate, Rating: e.Rating.Average, Runtime: e.Runtime}
	if e.Image != nil {
		em.StillURL = e.Image.Original
		if em.StillURL == "" {
			em.StillURL = e.Image.Medium
		}
	}
	return em
}
