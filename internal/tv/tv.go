// Package tv schedules Live TV: channels that play the library at random,
// like broadcast TV. A channel's schedule is a pure function of the channel,
// the day and the library, so every viewer tuning in sees the same program
// at the same point, and nothing has to run while nobody watches.
package tv

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"time"

	"lex/internal/store"
)

// Channel is a source of programs: one an admin made, the whole server, a
// library or a genre.
type Channel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"name"`

	filter store.AirFilter
}

// Program is one slot in a schedule. A program that would run past midnight
// is cut there: each day starts afresh.
type Program struct {
	ItemID int64
	Start  time.Time
	End    time.Time
}

// Genre channels: at most this many, each with at least minGenreTitles titles.
const (
	maxGenreChannels = 8
	minGenreTitles   = 5
)

type Guide struct {
	St *store.Store

	mu          sync.Mutex
	days        map[string][]Program // channel id + filter + date
	channels    []Channel
	channelsAt  time.Time
	channelsFor string // the admin's channels (and hidden ones) the list was made from
}

func New(st *store.Store) *Guide { return &Guide{St: st, days: map[string][]Program{}} }

// Channels lists the admin's channels (Settings › Server), then the
// automatic ones the admin hasn't turned off. A channel with nothing to air
// (e.g. a library still scanning) is still listed here, with no programs, so
// numbers don't shift. The list is kept for a minute, or until the admin's
// channels change: the guide and every tune ask.
func (g *Guide) Channels() ([]Channel, error) {
	cfg := g.St.Config()
	sig := fmt.Sprint(cfg.TVChannels, cfg.TVHidden)
	g.mu.Lock()
	if time.Since(g.channelsAt) < time.Minute && g.channelsFor == sig {
		defer g.mu.Unlock()
		return g.channels, nil
	}
	g.mu.Unlock()
	var out []Channel
	for _, c := range cfg.TVChannels {
		f := store.AirFilter{Libraries: c.Libraries, Genres: c.Genres, Kind: c.Kind, Exclude: c.Exclude}
		if c.Kind == "picked" {
			f = store.AirFilter{Kind: c.Kind, Titles: c.Titles}
		}
		out = append(out, Channel{ID: "custom-" + c.ID, Name: c.Name, filter: f})
	}
	auto, err := g.Automatic()
	if err != nil {
		return nil, err
	}
	for _, c := range auto {
		if !slices.Contains(cfg.TVHidden, c.ID) {
			out = append(out, c)
		}
	}
	for i := range out {
		out[i].Number = i + 1
	}
	g.mu.Lock()
	g.channels, g.channelsAt, g.channelsFor = out, time.Now(), sig
	g.mu.Unlock()
	return out, nil
}

// Automatic lists every channel the library could fill, hidden or not, and
// unnumbered: All (with more than one library), one per library, then the
// most common genres.
func (g *Guide) Automatic() ([]Channel, error) {
	libs, err := g.St.Libraries()
	if err != nil {
		return nil, err
	}
	var out []Channel
	if len(libs) > 1 {
		out = append(out, Channel{ID: "all", Name: "All"})
	}
	for _, l := range libs {
		out = append(out, Channel{ID: "lib-" + strconv.FormatInt(l.ID, 10), Name: l.Name, filter: store.AirFilter{Libraries: []int64{l.ID}}})
	}
	genres, err := g.St.TopGenres(maxGenreChannels, minGenreTitles)
	if err != nil {
		return nil, err
	}
	for _, name := range genres {
		out = append(out, Channel{ID: "genre-" + name, Name: name, filter: store.AirFilter{Genres: []string{name}}})
	}
	return out, nil
}

// Channel finds a channel by id.
func (g *Guide) Channel(id string) (Channel, bool, error) {
	chs, err := g.Channels()
	if err != nil {
		return Channel{}, false, err
	}
	for _, c := range chs {
		if c.ID == id {
			return c, true, nil
		}
	}
	return Channel{}, false, nil
}

// Programs returns the programs airing at some point in [from, to), in
// order; empty if the channel has nothing to air.
func (g *Guide) Programs(ch Channel, from, to time.Time) ([]Program, error) {
	var out []Program
	for day := midnight(from); day.Before(to); day = nextMidnight(day) {
		progs, err := g.day(ch, day)
		if err != nil {
			return nil, err
		}
		for _, p := range progs {
			if p.End.After(from) && p.Start.Before(to) {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// Next returns up to n programs from the one airing at t on, looking no
// further than the day after t's (so tomorrow's schedule is only made once
// it is needed).
func (g *Guide) Next(ch Channel, t time.Time, n int) ([]Program, error) {
	var out []Program
	for day, i := midnight(t), 0; i < 2 && len(out) < n; day, i = nextMidnight(day), i+1 {
		progs, err := g.day(ch, day)
		if err != nil {
			return nil, err
		}
		for _, p := range progs {
			if p.End.After(t) && len(out) < n {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// day returns a channel's schedule for the day starting at midnight start.
// It is kept for the day, so a library scan doesn't reshuffle what's on (a
// title removed meanwhile leaves its slot empty). Editing a channel makes a
// new one.
func (g *Guide) day(ch Channel, start time.Time) ([]Program, error) {
	date := start.Format("2006-01-02")
	key := ch.ID + "|" + date
	cacheKey := ch.ID + "|" + fmt.Sprint(ch.filter) + "|" + date
	g.mu.Lock()
	progs, ok := g.days[cacheKey]
	g.mu.Unlock()
	if ok {
		return progs, nil
	}
	pool, err := g.St.Airables(ch.filter)
	if err != nil {
		return nil, err
	}
	progs = Schedule(pool, seed(key), start, nextMidnight(start))
	g.mu.Lock()
	defer g.mu.Unlock()
	// Keep yesterday's and tomorrow's (a guide crossing midnight), not older.
	for k := range g.days {
		if d := k[len(k)-len(date):]; d < start.AddDate(0, 0, -1).Format("2006-01-02") {
			delete(g.days, k)
		}
	}
	g.days[cacheKey] = progs
	return progs, nil
}

func seed(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return h.Sum64()
}

// Schedule fills [start, end) from pool. Each slot picks a title (a movie or
// a show) at random, so a long-running show doesn't crowd out the rest, and
// for a show a random episode, preferring regular seasons over specials.
// Titles aired in the last few slots aren't picked again if others can be.
// The last program is cut at end.
func Schedule(pool []store.Airable, seed uint64, start, end time.Time) []Program {
	titles := map[int64][]store.Airable{}
	var keys []int64
	for _, a := range pool {
		k := -a.ItemID
		if a.ShowID > 0 {
			k = a.ShowID
		}
		if _, ok := titles[k]; !ok {
			keys = append(keys, k)
		}
		titles[k] = append(titles[k], a)
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	avoid := min(len(keys)/2, 6)
	var recent []int64
	var out []Program
	for t := start; t.Before(end); {
		k := keys[rng.IntN(len(keys))]
		for try := 0; try < 8 && slices.Contains(recent, k); try++ {
			k = keys[rng.IntN(len(keys))]
		}
		if avoid > 0 {
			recent = append(recent, k)
			if len(recent) > avoid {
				recent = recent[1:]
			}
		}
		eps := titles[k]
		if regular := slices.DeleteFunc(slices.Clone(eps), func(a store.Airable) bool { return a.ShowID > 0 && a.Season == 0 }); len(regular) > 0 {
			eps = regular
		}
		a := eps[rng.IntN(len(eps))]
		stop := t.Add(time.Duration(a.Duration * float64(time.Second)))
		if stop.After(end) {
			stop = end
		}
		out = append(out, Program{ItemID: a.ItemID, Start: t, End: stop})
		t = stop
	}
	return out
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func nextMidnight(day time.Time) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, day.Location())
}
