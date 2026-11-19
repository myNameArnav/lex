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
// library or a genre. Its number is its place in the lineup, counting the
// channels turned off, so turning one off doesn't renumber the rest.
type Channel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"name"`

	// Off: turned off by the admin (only Lineup lists these).
	Off bool `json:"-"`
	// Stream is set for a live channel from the internet: it has no
	// schedule of library titles, only its guide's programs.
	Stream *store.TVStream `json:"-"`
	filter store.AirFilter
}

// Filter is what an admin's channel airs.
func Filter(c store.TVChannel) store.AirFilter {
	if c.Kind == "picked" {
		return store.AirFilter{Kind: c.Kind, Titles: c.Titles}
	}
	return store.AirFilter{Libraries: c.Libraries, Genres: c.Genres, Kind: c.Kind, Exclude: c.Exclude}
}

// StreamPrograms returns a live channel's guide programs in [from, to):
// none until its source's guide has been read, or without a guide id.
func (g *Guide) StreamPrograms(ch Channel, from, to time.Time) []GuideProgram {
	if ch.Stream == nil {
		return nil
	}
	return g.guideFor(ch.Stream, from, to)
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
	// Dir holds uploaded playlists and guides.
	Dir string

	mu        sync.Mutex
	days      map[string][]Program // channel id + filter + date
	lineup    []Channel
	lineupAt  time.Time
	lineupFor string // the lineup settings the list was made from
	pools     map[string]poolInfo
	playlists map[string]playlist
	epg       epg
}

// poolInfo sums up what a channel can air.
type poolInfo struct {
	libs   []int64
	titles int
	at     time.Time
}

func New(st *store.Store) *Guide {
	return &Guide{St: st, days: map[string][]Program{}, pools: map[string]poolInfo{}, playlists: map[string]playlist{}}
}

// Channels lists the channels on air, in lineup order. A channel with
// nothing to air (e.g. a library still scanning) is still listed here, with
// no programs, so numbers don't shift.
func (g *Guide) Channels() ([]Channel, error) {
	all, err := g.Lineup()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(all), func(c Channel) bool { return c.Off }), nil
}

// Lineup lists every channel, including the ones turned off: the order the
// admin set (Settings › Live TV), then the channels it doesn't name yet: the
// admin's own, then the automatic ones the admin added (none are on the
// lineup until added). The list is kept for a minute, or
// until the lineup settings change: the guide and every tune ask.
func (g *Guide) Lineup() ([]Channel, error) {
	cfg := g.St.Config()
	sig := fmt.Sprint(cfg.TVChannels, cfg.TVStreams, cfg.TVOrder, cfg.TVHidden, cfg.TVAutomatic)
	g.mu.Lock()
	if time.Since(g.lineupAt) < time.Minute && g.lineupFor == sig {
		defer g.mu.Unlock()
		return g.lineup, nil
	}
	g.mu.Unlock()
	var rest []Channel
	for _, c := range cfg.TVChannels {
		rest = append(rest, Channel{ID: "custom-" + c.ID, Name: c.Name, filter: Filter(c)})
	}
	for _, st := range cfg.TVStreams {
		rest = append(rest, Channel{ID: "stream-" + st.ID, Name: st.Name, Stream: &st})
	}
	auto, err := g.Automatic()
	if err != nil {
		return nil, err
	}
	for _, c := range auto {
		if slices.Contains(cfg.TVAutomatic, c.ID) {
			rest = append(rest, c)
		}
	}
	out := make([]Channel, 0, len(rest))
	for _, id := range cfg.TVOrder {
		if i := slices.IndexFunc(rest, func(c Channel) bool { return c.ID == id }); i >= 0 {
			out = append(out, rest[i])
			rest = slices.Delete(rest, i, i+1)
		}
	}
	out = append(out, rest...)
	for i := range out {
		out[i].Number = i + 1
		out[i].Off = slices.Contains(cfg.TVHidden, out[i].ID)
	}
	g.mu.Lock()
	g.lineup, g.lineupAt, g.lineupFor = out, time.Now(), sig
	g.mu.Unlock()
	return out, nil
}

// Automatic lists every channel the library could fill, for the admin to
// add, unnumbered: All
// (with more than one library), one per library, then the most common
// genres.
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

// Channel finds a channel on air by id.
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

// Libraries lists the libraries a channel's titles come from, so a channel
// can be hidden from users who can't see all of them (every viewer sees the
// same schedule, so it can't leave out what they can't see).
func (g *Guide) Libraries(ch Channel) ([]int64, error) {
	if ch.Stream != nil {
		return []int64{}, nil
	}
	p, err := g.pool(ch.ID, ch.filter)
	return p.libs, err
}

// Titles counts the movies and shows a channel can air.
func (g *Guide) Titles(ch Channel) (int, error) {
	if ch.Stream != nil {
		return 0, nil
	}
	p, err := g.pool(ch.ID, ch.filter)
	return p.titles, err
}

// pool sums up what a filter airs. Kept for a minute.
func (g *Guide) pool(id string, f store.AirFilter) (poolInfo, error) {
	key := id + "|" + fmt.Sprint(f)
	g.mu.Lock()
	c, ok := g.pools[key]
	g.mu.Unlock()
	if ok && time.Since(c.at) < time.Minute {
		return c, nil
	}
	pool, err := g.St.Airables(f)
	if err != nil {
		return poolInfo{}, err
	}
	p := poolInfo{libs: []int64{}, titles: countTitles(pool), at: time.Now()}
	for _, a := range pool {
		if !slices.Contains(p.libs, a.LibraryID) {
			p.libs = append(p.libs, a.LibraryID)
		}
	}
	slices.Sort(p.libs)
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.pools {
		if time.Since(v.at) >= time.Minute {
			delete(g.pools, k)
		}
	}
	g.pools[key] = p
	return p, nil
}

// countTitles counts the movies and shows in a pool.
func countTitles(pool []store.Airable) int {
	seen := map[int64]bool{}
	for _, a := range pool {
		k := -a.ItemID
		if a.ShowID > 0 {
			k = a.ShowID
		}
		seen[k] = true
	}
	return len(seen)
}

// Preview schedules an admin's channel as it would air if saved now (id is
// its channel id, so an unchanged channel previews what it airs): how many
// titles it has and up to n programs from t on, into tomorrow if need be.
// Nothing is kept.
func (g *Guide) Preview(id string, f store.AirFilter, t time.Time, n int) (int, []Program, error) {
	pool, err := g.St.Airables(f)
	if err != nil {
		return 0, nil, err
	}
	// Today's schedule from t, then tomorrow's (late in the evening).
	var out []Program
	for day, i := midnight(t), 0; i < 2 && len(out) < n; day, i = nextMidnight(day), i+1 {
		for _, p := range Schedule(pool, seed(id+"|"+day.Format("2006-01-02")), day, nextMidnight(day)) {
			if p.End.After(t) && len(out) < n {
				out = append(out, p)
			}
		}
	}
	return countTitles(pool), out, nil
}

// Programs returns the programs airing at some point in [from, to), in
// order; empty if the channel has nothing to air.
func (g *Guide) Programs(ch Channel, from, to time.Time) ([]Program, error) {
	if ch.Stream != nil {
		return nil, nil
	}
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
	if ch.Stream != nil {
		return nil, nil
	}
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
