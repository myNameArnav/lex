package meta

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"lex/internal/store"
)

const tmdbBase = "https://api.themoviedb.org/3"
const tmdbImg = "https://image.tmdb.org/t/p/"

type TMDB struct {
	Key  string
	Lang string
	lim  limiter
}

func NewTMDB(key, lang string) *TMDB {
	return &TMDB{Key: strings.TrimSpace(key), Lang: lang, lim: limiter{gap: 60 * time.Millisecond}}
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values, v any) error {
	if q == nil {
		q = url.Values{}
	}
	if t.Lang != "" {
		q.Set("language", t.Lang)
	}
	headers := map[string]string{}
	// v4 read-access tokens are JWTs; v3 keys are 32 hex chars.
	if strings.HasPrefix(t.Key, "eyJ") {
		headers["Authorization"] = "Bearer " + t.Key
	} else {
		q.Set("api_key", t.Key)
	}
	return getJSON(ctx, &t.lim, tmdbBase+path+"?"+q.Encode(), headers, v)
}

func tmdbImage(size, p string) string {
	if p == "" {
		return ""
	}
	return tmdbImg + size + p
}

type tmdbSearch struct {
	Results []struct {
		ID            int     `json:"id"`
		Title         string  `json:"title"`
		Name          string  `json:"name"`
		OriginalTitle string  `json:"original_title"`
		OriginalName  string  `json:"original_name"`
		ReleaseDate   string  `json:"release_date"`
		FirstAirDate  string  `json:"first_air_date"`
		Overview      string  `json:"overview"`
		PosterPath    string  `json:"poster_path"`
		Popularity    float64 `json:"popularity"`
	} `json:"results"`
}

func (t *TMDB) Search(ctx context.Context, kind, title string, year int) ([]Candidate, error) {
	path := "/search/movie"
	yearKey := "primary_release_year"
	if kind == "show" {
		path, yearKey = "/search/tv", "first_air_date_year"
	}
	q := url.Values{"query": {title}, "include_adult": {"false"}}
	if year > 0 {
		q.Set(yearKey, strconv.Itoa(year))
	}
	var r tmdbSearch
	if err := t.get(ctx, path, q, &r); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, x := range r.Results {
		c := Candidate{Provider: "tmdb", ID: strconv.Itoa(x.ID), Kind: kind, Overview: x.Overview, Poster: tmdbImage("w185", x.PosterPath)}
		if kind == "show" {
			c.Title, c.Year = x.Name, yearOf(x.FirstAirDate)
		} else {
			c.Title, c.Year = x.Title, yearOf(x.ReleaseDate)
		}
		out = append(out, c)
	}
	return out, nil
}

// Find resolves an external id (imdb/tvdb) to a TMDB id.
func (t *TMDB) Find(ctx context.Context, kind, source, id string) (string, error) {
	var r struct {
		Movie []struct{ ID int } `json:"movie_results"`
		TV    []struct{ ID int } `json:"tv_results"`
	}
	if err := t.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &r); err != nil {
		return "", err
	}
	if kind == "show" && len(r.TV) > 0 {
		return strconv.Itoa(r.TV[0].ID), nil
	}
	if kind == "movie" && len(r.Movie) > 0 {
		return strconv.Itoa(r.Movie[0].ID), nil
	}
	return "", ErrNoMatch
}

type tmdbCredits struct {
	Cast []struct {
		Name        string `json:"name"`
		Character   string `json:"character"`
		ProfilePath string `json:"profile_path"`
	} `json:"cast"`
	Crew []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
}

func (c tmdbCredits) people(n int) []store.Person {
	var out []store.Person
	for _, cr := range c.Crew {
		if cr.Job == "Director" && len(out) < 2 {
			out = append(out, store.Person{Name: cr.Name, Role: "Director"})
		}
	}
	for i, x := range c.Cast {
		if i >= n {
			break
		}
		out = append(out, store.Person{Name: x.Name, Role: x.Character, Image: tmdbImage("w185", x.ProfilePath)})
	}
	return out
}

type named struct {
	Name string `json:"name"`
}

func names(n []named) []string {
	var out []string
	for _, x := range n {
		out = append(out, x.Name)
	}
	return out
}

func (t *TMDB) Movie(ctx context.Context, id string) (*Result, error) {
	var m struct {
		ID                  int         `json:"id"`
		Title               string      `json:"title"`
		OriginalTitle       string      `json:"original_title"`
		Overview            string      `json:"overview"`
		Tagline             string      `json:"tagline"`
		ReleaseDate         string      `json:"release_date"`
		Runtime             int         `json:"runtime"`
		VoteAverage         float64     `json:"vote_average"`
		Genres              []named     `json:"genres"`
		ProductionCompanies []named     `json:"production_companies"`
		PosterPath          string      `json:"poster_path"`
		BackdropPath        string      `json:"backdrop_path"`
		IMDBID              string      `json:"imdb_id"`
		Credits             tmdbCredits `json:"credits"`
		BelongsTo           *struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"belongs_to_collection"`
		ReleaseDates struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Certification string `json:"certification"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := t.get(ctx, "/movie/"+url.PathEscape(id), url.Values{"append_to_response": {"credits,release_dates"}}, &m); err != nil {
		return nil, err
	}
	r := &Result{
		Provider: "tmdb", ID: strconv.Itoa(m.ID), Title: m.Title, OriginalTitle: m.OriginalTitle, Overview: m.Overview, Tagline: m.Tagline,
		Year: yearOf(m.ReleaseDate), Premiere: m.ReleaseDate, Runtime: m.Runtime, Rating: m.VoteAverage,
		Genres: names(m.Genres), Studios: names(m.ProductionCompanies), Cast: m.Credits.people(20),
		PosterURL: tmdbImage("w500", m.PosterPath), BackdropURL: tmdbImage("w1280", m.BackdropPath),
		IDs: map[string]string{"tmdb": strconv.Itoa(m.ID)},
	}
	if m.IMDBID != "" {
		r.IDs["imdb"] = m.IMDBID
	}
	if b := m.BelongsTo; b != nil && b.ID > 0 && b.Name != "" {
		r.Collection = &store.Collection{ID: b.ID, Name: b.Name}
	}
	for _, c := range m.ReleaseDates.Results {
		if c.Country == "US" {
			for _, d := range c.Dates {
				if d.Certification != "" {
					r.ContentRating = d.Certification
					break
				}
			}
		}
	}
	return r, nil
}

func (t *TMDB) Show(ctx context.Context, id string) (*Result, error) {
	var m struct {
		ID             int         `json:"id"`
		Name           string      `json:"name"`
		OriginalName   string      `json:"original_name"`
		Overview       string      `json:"overview"`
		Tagline        string      `json:"tagline"`
		FirstAirDate   string      `json:"first_air_date"`
		EpisodeRunTime []int       `json:"episode_run_time"`
		VoteAverage    float64     `json:"vote_average"`
		Genres         []named     `json:"genres"`
		Networks       []named     `json:"networks"`
		PosterPath     string      `json:"poster_path"`
		BackdropPath   string      `json:"backdrop_path"`
		Credits        tmdbCredits `json:"credits"`
		ExternalIDs    struct {
			IMDB string `json:"imdb_id"`
			TVDB int    `json:"tvdb_id"`
		} `json:"external_ids"`
		ContentRatings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
	}
	if err := t.get(ctx, "/tv/"+url.PathEscape(id), url.Values{"append_to_response": {"credits,external_ids,content_ratings"}}, &m); err != nil {
		return nil, err
	}
	r := &Result{
		Provider: "tmdb", ID: strconv.Itoa(m.ID), Title: m.Name, OriginalTitle: m.OriginalName, Overview: m.Overview, Tagline: m.Tagline,
		Year: yearOf(m.FirstAirDate), Premiere: m.FirstAirDate, Rating: m.VoteAverage,
		Genres: names(m.Genres), Studios: names(m.Networks),
		PosterURL: tmdbImage("w500", m.PosterPath), BackdropURL: tmdbImage("w1280", m.BackdropPath),
		IDs: map[string]string{"tmdb": strconv.Itoa(m.ID)},
	}
	if len(m.EpisodeRunTime) > 0 {
		r.Runtime = m.EpisodeRunTime[0]
	}
	r.Cast = m.Credits.people(20)
	if m.ExternalIDs.IMDB != "" {
		r.IDs["imdb"] = m.ExternalIDs.IMDB
	}
	if m.ExternalIDs.TVDB > 0 {
		r.IDs["tvdb"] = strconv.Itoa(m.ExternalIDs.TVDB)
	}
	for _, c := range m.ContentRatings.Results {
		if c.Country == "US" {
			r.ContentRating = c.Rating
		}
	}
	return r, nil
}

func (t *TMDB) Season(ctx context.Context, id string, season int) (*SeasonMeta, error) {
	var m struct {
		Name       string `json:"name"`
		Overview   string `json:"overview"`
		PosterPath string `json:"poster_path"`
		AirDate    string `json:"air_date"`
		Episodes   []struct {
			EpisodeNumber int     `json:"episode_number"`
			Name          string  `json:"name"`
			Overview      string  `json:"overview"`
			AirDate       string  `json:"air_date"`
			StillPath     string  `json:"still_path"`
			VoteAverage   float64 `json:"vote_average"`
			Runtime       int     `json:"runtime"`
		} `json:"episodes"`
	}
	if err := t.get(ctx, fmt.Sprintf("/tv/%s/season/%d", url.PathEscape(id), season), nil, &m); err != nil {
		return nil, err
	}
	sm := &SeasonMeta{Season: season, Title: m.Name, Overview: m.Overview, PosterURL: tmdbImage("w500", m.PosterPath), Premiere: m.AirDate}
	for _, e := range m.Episodes {
		sm.Episodes = append(sm.Episodes, EpisodeMeta{
			Season: season, Episode: e.EpisodeNumber, Title: e.Name, Overview: e.Overview, Premiere: e.AirDate,
			StillURL: tmdbImage("w400", e.StillPath), Rating: e.VoteAverage, Runtime: e.Runtime,
		})
	}
	return sm, nil
}

// tmdbSeason is a season in TMDB's show details.
type tmdbSeason struct {
	Number  int    `json:"season_number"`
	AirDate string `json:"air_date"`
}

// EpisodesByDate finds the regular episodes that aired on the given dates
// (YYYY-MM-DD). TMDB lists episodes per season, so each date is looked up
// in the season that started last on or before it, then in the next one
// (season start dates are sometimes later than their first episode).
func (t *TMDB) EpisodesByDate(ctx context.Context, id string, dates []string) (map[string]EpisodeMeta, error) {
	var m struct {
		Seasons []tmdbSeason `json:"seasons"`
	}
	if err := t.get(ctx, "/tv/"+url.PathEscape(id), nil, &m); err != nil {
		return nil, err
	}
	fetched := map[int]map[string]EpisodeMeta{}
	season := func(n int) (map[string]EpisodeMeta, error) {
		if byDate, ok := fetched[n]; ok {
			return byDate, nil
		}
		byDate := map[string]EpisodeMeta{}
		sm, err := t.Season(ctx, id, n)
		if err != nil && !errors.Is(err, ErrNoMatch) {
			return nil, err
		}
		if sm != nil {
			for _, e := range sm.Episodes {
				if cur, ok := byDate[e.Premiere]; e.Premiere != "" && (!ok || e.Episode < cur.Episode) {
					byDate[e.Premiere] = e
				}
			}
		}
		fetched[n] = byDate
		return byDate, nil
	}
	out := map[string]EpisodeMeta{}
	for _, d := range dates {
		for _, n := range tmdbSeasonsFor(m.Seasons, d) {
			byDate, err := season(n)
			if err != nil {
				return out, err
			}
			if e, ok := byDate[d]; ok {
				out[d] = e
				break
			}
		}
	}
	return out, nil
}

// tmdbSeasonsFor returns the regular seasons an episode aired on date most
// likely belongs to: the one that started last on or before it, then the
// one after that.
func tmdbSeasonsFor(seasons []tmdbSeason, date string) []int {
	regular := make([]tmdbSeason, 0, len(seasons))
	for _, s := range seasons {
		if s.Number > 0 && s.AirDate != "" {
			regular = append(regular, s)
		}
	}
	sort.Slice(regular, func(i, j int) bool { return regular[i].AirDate < regular[j].AirDate })
	i := sort.Search(len(regular), func(i int) bool { return regular[i].AirDate > date })
	var out []int
	if i > 0 {
		out = append(out, regular[i-1].Number)
	}
	if i < len(regular) {
		out = append(out, regular[i].Number)
	}
	return out
}
