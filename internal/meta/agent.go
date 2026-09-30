package meta

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"lex/internal/library"
	"lex/internal/logx"
	"lex/internal/store"
)

type AgentStatus struct {
	Running   bool   `json:"running"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	Matched   int    `json:"matched"`
	Missing   int    `json:"missing"`
	Current   string `json:"current"`
	LastRun   int64  `json:"lastRun"`
	LastError string `json:"lastError,omitempty"`
}

type Agent struct {
	st      *store.Store
	Images  *ImageCache
	log     *logx.Logger
	tvmaze  *TVmaze
	radarrC *Radarr

	mu     sync.Mutex
	status AgentStatus
	queued bool
	run    sync.Mutex
}

func NewAgent(st *store.Store, images *ImageCache, log *logx.Logger) *Agent {
	return &Agent{st: st, Images: images, log: log, tvmaze: NewTVmaze(), radarrC: NewRadarr()}
}

func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

// radarr returns the Radarr client if one is configured.
func (a *Agent) radarr() *Radarr {
	c := a.st.Config()
	if strings.TrimSpace(c.RadarrURL) == "" || strings.TrimSpace(c.RadarrKey) == "" {
		return nil
	}
	a.radarrC.SetEndpoint(strings.TrimSpace(c.RadarrURL), strings.TrimSpace(c.RadarrKey))
	return a.radarrC
}

func (a *Agent) tmdb() *TMDB {
	c := a.st.Config()
	if !c.EnableTMDB || strings.TrimSpace(c.TMDBKey) == "" {
		return nil
	}
	return NewTMDB(c.TMDBKey, c.MetadataLanguage)
}

// Trigger processes all pending items in the background.
func (a *Agent) Trigger() {
	a.mu.Lock()
	if a.status.Running {
		a.queued = true
		a.mu.Unlock()
		return
	}
	a.status.Running = true
	a.mu.Unlock()
	go a.Run(context.Background())
}

func (a *Agent) Run(ctx context.Context) {
	a.run.Lock()
	defer a.run.Unlock()
	for {
		a.tvmaze.ResetCache()
		items, err := a.st.ItemsNeedingMetadata()
		a.mu.Lock()
		a.status = AgentStatus{Running: true, Total: len(items)}
		a.mu.Unlock()
		if err != nil {
			a.log.Errorf("metadata: %v", err)
		}
		if len(items) > 0 {
			a.log.Infof("metadata: %d items pending", len(items))
		}
		doneShows := map[int64]bool{}
		for _, it := range items {
			if ctx.Err() != nil {
				break
			}
			a.mu.Lock()
			a.status.Current = it.Title
			a.mu.Unlock()
			var ok bool
			switch it.Kind {
			case "movie":
				ok, err = a.refreshMovie(ctx, it)
			case "show":
				ok, err = a.refreshShow(ctx, it)
				doneShows[it.ID] = true
			case "season", "episode":
				if doneShows[it.ShowID] {
					a.bump(true)
					continue
				}
				doneShows[it.ShowID] = true
				var show *store.Item
				show, err = a.st.Item(it.ShowID)
				if err == nil {
					err = a.refreshChildren(ctx, show, true)
					ok = err == nil
				}
			}
			if err != nil && !errors.Is(err, ErrNoMatch) {
				a.log.Warnf("metadata %q: %v", it.Title, err)
				a.mu.Lock()
				a.status.LastError = err.Error()
				a.mu.Unlock()
			}
			a.bump(ok)
		}
		a.mu.Lock()
		again := a.queued
		a.queued = false
		a.status.Running = again
		a.status.Current = ""
		a.status.LastRun = time.Now().Unix()
		a.mu.Unlock()
		if !again {
			return
		}
	}
}

func (a *Agent) bump(ok bool) {
	a.mu.Lock()
	a.status.Done++
	if ok {
		a.status.Matched++
	} else {
		a.status.Missing++
	}
	a.mu.Unlock()
}

// ---- scoring ----

func tokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(library.SearchTitle(s))) {
		t = library.Normalize(t)
		if t != "" {
			m[t] = true
		}
	}
	return m
}

func score(c Candidate, title string, year, pos int) int {
	n1, n2 := library.Normalize(c.Title), library.Normalize(title)
	s := 0
	switch {
	case n1 == n2:
		s = 10
	case n1 != "" && n2 != "" && (strings.Contains(n1, n2) || strings.Contains(n2, n1)):
		s = 6
	default:
		a, b := tokens(c.Title), tokens(title)
		inter := 0
		for t := range a {
			if b[t] {
				inter++
			}
		}
		union := len(a) + len(b) - inter
		if union > 0 {
			s = inter * 8 / union
		}
	}
	if year > 0 && c.Year > 0 {
		d := year - c.Year
		if d < 0 {
			d = -d
		}
		switch {
		case d == 0:
			s += 5
		case d == 1:
			s += 2
		default:
			s -= 4
		}
	}
	if pos == 0 {
		s++
	}
	return s
}

func best(cands []Candidate, title string, year int) *Candidate {
	var top *Candidate
	for i := range cands {
		cands[i].score = score(cands[i], title, year, i)
		if top == nil || cands[i].score > top.score {
			top = &cands[i]
		}
	}
	if top == nil || top.score < 6 {
		return nil
	}
	return top
}

type searcher func(ctx context.Context, title string, year int) ([]Candidate, error)

// searchBest tries a few query variants until something scores well.
func searchBest(ctx context.Context, fn searcher, title string, year int) (*Candidate, error) {
	queries := []string{library.SearchTitle(title)}
	if i := strings.IndexAny(title, ":-"); i > 3 {
		queries = append(queries, library.SearchTitle(title[:i]))
	}
	var lastErr error
	for _, q := range queries {
		for _, y := range []int{year, 0} {
			if y == 0 && year == 0 && q != queries[0] {
				continue
			}
			c, err := fn(ctx, q, y)
			if err != nil {
				lastErr = err
				continue
			}
			if b := best(c, title, year); b != nil {
				return b, nil
			}
			if year == 0 {
				break
			}
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNoMatch
}

// ---- movies ----

func (a *Agent) refreshMovie(ctx context.Context, it *store.Item) (bool, error) {
	cfg := a.st.Config()
	files, _ := a.st.ItemFiles(it.ID)
	var localPoster, localBackdrop string
	if cfg.UseLocalMetadata && len(files) > 0 {
		localPoster, localBackdrop = localMovieArt(it.Path, files[0].Path)
	}
	title, year := it.Hint.Title, it.Hint.Year
	if title == "" {
		title, year = it.Title, it.Year
	}
	var res *Result
	var err error
	pid := it.ProviderIDs
	if pid == nil {
		pid = map[string]string{}
	}
	src := pid["source"]
	if t := a.tmdb(); t != nil && (src == "" || src == "tmdb") {
		id := firstNonEmpty(pid["tmdb"], it.Hint.TMDB)
		if id == "" && it.Hint.IMDB != "" {
			id, _ = t.Find(ctx, "movie", "imdb_id", it.Hint.IMDB)
		}
		if id == "" {
			var c *Candidate
			if c, err = searchBest(ctx, t.Search2("movie"), title, year); c != nil {
				id = c.ID
			}
		}
		if id != "" {
			res, err = t.Movie(ctx, id)
		}
	}
	if r := a.radarr(); r != nil && res == nil && (src == "" || src == "radarr" || src == "tmdb") {
		// Radarr ids are TMDB ids.
		id := firstNonEmpty(pid["radarr"], pid["tmdb"], it.Hint.TMDB)
		if id == "" && len(files) > 0 {
			id = r.MatchFolder(ctx, folderOf(it, files[0].Path))
		}
		if id == "" && it.Hint.IMDB != "" {
			id = r.FindIMDB(ctx, it.Hint.IMDB)
		}
		if id == "" {
			var c *Candidate
			var e2 error
			if c, e2 = searchBest(ctx, r.Search, title, year); c != nil {
				id = c.ID
			} else if err == nil {
				err = e2
			}
		}
		if id != "" {
			res, err = r.Movie(ctx, id)
		}
	}
	if res == nil {
		it.Poster, it.Backdrop = localPoster, localBackdrop
		it.MetaStatus = missingStatus(err, localPoster)
		if e := a.st.SaveMetadata(it); e != nil {
			return false, e
		}
		if err == nil {
			err = ErrNoMatch
		}
		return false, err
	}
	a.apply(it, res)
	if localPoster != "" {
		it.Poster = localPoster
	}
	if localBackdrop != "" {
		it.Backdrop = localBackdrop
	}
	return true, a.st.SaveMetadata(it)
}

// missingStatus keeps items pending after transient (network) failures so
// the next run retries them; genuine misses are recorded as not found.
func missingStatus(err error, localPoster string) int {
	if err != nil && !errors.Is(err, ErrNoMatch) {
		return store.MetaPending
	}
	if localPoster != "" {
		return store.MetaLocal
	}
	return store.MetaNotFound
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Search2 adapts TMDB.Search to the searcher signature.
func (t *TMDB) Search2(kind string) searcher {
	return func(ctx context.Context, title string, year int) ([]Candidate, error) {
		return t.Search(ctx, kind, title, year)
	}
}

func (a *Agent) apply(it *store.Item, r *Result) {
	it.Title = r.Title
	it.SortTitle = store.SortTitle(r.Title)
	if r.OriginalTitle != r.Title {
		it.OriginalTitle = r.OriginalTitle
	} else {
		it.OriginalTitle = ""
	}
	if r.Year > 0 {
		it.Year = r.Year
	}
	it.Overview, it.Tagline, it.Rating, it.ContentRating = r.Overview, r.Tagline, r.Rating, r.ContentRating
	it.Genres, it.Studios, it.Cast, it.Runtime, it.Premiere = r.Genres, r.Studios, r.Cast, r.Runtime, r.Premiere
	it.Poster, it.Backdrop = urlRef(r.PosterURL), urlRef(r.BackdropURL)
	ids := map[string]string{}
	for k, v := range it.ProviderIDs {
		ids[k] = v
	}
	for k, v := range r.IDs {
		ids[k] = v
	}
	ids["source"] = r.Provider
	it.ProviderIDs = ids
	it.MetaStatus = store.MetaMatched
}

func urlRef(u string) string {
	if u == "" {
		return ""
	}
	return "url:" + u
}

// ---- shows ----

type showSource interface {
	Season(ctx context.Context, id string, season int) (*SeasonMeta, error)
}

func (a *Agent) refreshShow(ctx context.Context, it *store.Item) (bool, error) {
	cfg := a.st.Config()
	title, year := it.Hint.Title, it.Hint.Year
	if title == "" {
		title, year = it.Title, it.Year
	}
	pid := it.ProviderIDs
	if pid == nil {
		pid = map[string]string{}
	}
	src := pid["source"]
	var res *Result
	var err error
	if t := a.tmdb(); t != nil && (src == "" || src == "tmdb") {
		id := firstNonEmpty(pid["tmdb"], it.Hint.TMDB)
		if id == "" && it.Hint.TVDB != "" {
			id, _ = t.Find(ctx, "show", "tvdb_id", it.Hint.TVDB)
		}
		if id == "" && it.Hint.IMDB != "" {
			id, _ = t.Find(ctx, "show", "imdb_id", it.Hint.IMDB)
		}
		if id == "" {
			var c *Candidate
			if c, err = searchBest(ctx, t.Search2("show"), title, year); c != nil {
				id = c.ID
			}
		}
		if id != "" {
			res, err = t.Show(ctx, id)
		}
	}
	if res == nil && cfg.EnableTVmaze && (src == "" || src == "tvmaze") {
		id := pid["tvmaze"]
		if id == "" && it.Hint.TVDB != "" {
			id, _ = a.tvmaze.Lookup(ctx, "tvdb", it.Hint.TVDB)
		}
		if id == "" && it.Hint.IMDB != "" {
			id, _ = a.tvmaze.Lookup(ctx, "imdb", it.Hint.IMDB)
		}
		if id == "" {
			var c *Candidate
			var e2 error
			if c, e2 = searchBest(ctx, a.tvmaze.Search, title, year); c != nil {
				id = c.ID
			} else {
				err = e2
			}
		}
		if id != "" {
			res, err = a.tvmaze.Show(ctx, id)
		}
	}
	var localPoster, localBackdrop string
	if cfg.UseLocalMetadata {
		localPoster, localBackdrop = localShowArt(it.Path)
	}
	if res == nil {
		it.Poster, it.Backdrop = localPoster, localBackdrop
		it.MetaStatus = missingStatus(err, localPoster)
		a.st.SaveMetadata(it)
		a.refreshChildren(ctx, it, false)
		if err == nil {
			err = ErrNoMatch
		}
		return false, err
	}
	a.apply(it, res)
	if localPoster != "" {
		it.Poster = localPoster
	}
	if localBackdrop != "" {
		it.Backdrop = localBackdrop
	}
	if err := a.st.SaveMetadata(it); err != nil {
		return false, err
	}
	return true, a.refreshChildren(ctx, it, false)
}

func (a *Agent) showSource(show *store.Item) (showSource, string) {
	if show.MetaStatus != store.MetaMatched || show.ProviderIDs == nil {
		return nil, ""
	}
	switch show.ProviderIDs["source"] {
	case "tmdb":
		if t := a.tmdb(); t != nil {
			return t, show.ProviderIDs["tmdb"]
		}
	case "tvmaze":
		return a.tvmaze, show.ProviderIDs["tvmaze"]
	}
	return nil, ""
}

// refreshChildren fills season and episode metadata from the show's provider.
func (a *Agent) refreshChildren(ctx context.Context, show *store.Item, pendingOnly bool) error {
	cfg := a.st.Config()
	seasons, err := a.st.Children(show.ID, 0)
	if err != nil {
		return err
	}
	episodes, err := a.st.ShowEpisodes(show.ID, 0)
	if err != nil {
		return err
	}
	bySeason := map[int64][]*store.Item{}
	for _, e := range episodes {
		bySeason[e.ParentID] = append(bySeason[e.ParentID], e)
	}
	src, id := a.showSource(show)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Season < seasons[j].Season })
	for _, season := range seasons {
		eps := bySeason[season.ID]
		if pendingOnly {
			need := season.MetaStatus == store.MetaPending
			for _, e := range eps {
				need = need || e.MetaStatus == store.MetaPending
			}
			if !need {
				continue
			}
		}
		var sm *SeasonMeta
		if src != nil && id != "" {
			sm, err = src.Season(ctx, id, season.Season)
			if err != nil && !errors.Is(err, ErrNoMatch) {
				a.log.Warnf("metadata %s season %d: %v", show.Title, season.Season, err)
			}
		}
		poster := ""
		if cfg.UseLocalMetadata {
			poster = localSeasonArt(show.Path, season.Season)
		}
		if sm != nil {
			if sm.Title != "" {
				season.Title = sm.Title
			}
			season.Overview, season.Premiere = sm.Overview, sm.Premiere
			season.Poster = urlRef(sm.PosterURL)
			if season.Year == 0 {
				season.Year = yearOf(sm.Premiere)
			}
			season.MetaStatus = store.MetaMatched
		} else {
			season.MetaStatus = store.MetaNotFound
		}
		if poster != "" {
			season.Poster = poster
		}
		a.st.SaveMetadata(season)
		epMeta := map[int]EpisodeMeta{}
		if sm != nil {
			for _, em := range sm.Episodes {
				epMeta[em.Episode] = em
			}
		}
		for _, e := range eps {
			files, _ := a.st.ItemFiles(e.ID)
			thumb := ""
			if cfg.UseLocalMetadata && len(files) > 0 {
				thumb = localEpisodeThumb(files[0].Path)
			}
			if em, ok := epMeta[e.Episode]; ok {
				e.Title = em.Title
				if e.EpisodeEnd > e.Episode {
					if em2, ok := epMeta[e.EpisodeEnd]; ok && em2.Title != "" && em2.Title != em.Title {
						e.Title = em.Title + " / " + em2.Title
					}
				}
				e.Overview, e.Premiere, e.Rating, e.Runtime = em.Overview, em.Premiere, em.Rating, em.Runtime
				e.Year = yearOf(em.Premiere)
				e.Thumb = urlRef(em.StillURL)
				e.MetaStatus = store.MetaMatched
			} else {
				e.MetaStatus = store.MetaNotFound
			}
			if thumb != "" {
				e.Thumb = thumb
			}
			if e.Title == "" {
				e.Title = fmt.Sprintf("Episode %d", e.Episode)
			}
			a.st.SaveMetadata(e)
		}
	}
	return nil
}

// ---- manual matching ----

func (a *Agent) Search(ctx context.Context, kind, query string, year int) ([]Candidate, error) {
	cfg := a.st.Config()
	var out []Candidate
	var errs []string
	if t := a.tmdb(); t != nil {
		c, err := t.Search(ctx, kind, query, year)
		if err != nil {
			errs = append(errs, "tmdb: "+err.Error())
		}
		out = append(out, c...)
	}
	if kind == "show" && cfg.EnableTVmaze {
		c, err := a.tvmaze.Search(ctx, query, year)
		if err != nil {
			errs = append(errs, "tvmaze: "+err.Error())
		}
		out = append(out, c...)
	}
	if r := a.radarr(); kind == "movie" && r != nil {
		c, err := r.Search(ctx, query, year)
		if err != nil {
			errs = append(errs, "radarr: "+err.Error())
		}
		out = append(out, c...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

// Match pins an item to a provider id and refreshes it synchronously.
func (a *Agent) Match(ctx context.Context, itemID int64, provider, id string) error {
	it, err := a.st.Item(itemID)
	if err != nil {
		return err
	}
	it.ProviderIDs = map[string]string{provider: id, "source": provider}
	it.Hint.TMDB, it.Hint.TVDB, it.Hint.IMDB = "", "", ""
	it.MetaLocked = true
	it.MetaStatus = store.MetaPending
	if err := a.st.SaveMetadata(it); err != nil {
		return err
	}
	return a.Refresh(ctx, itemID)
}

// Refresh re-fetches one item (and its children) synchronously.
func (a *Agent) Refresh(ctx context.Context, itemID int64) error {
	it, err := a.st.Item(itemID)
	if err != nil {
		return err
	}
	a.tvmaze.ResetCache()
	switch it.Kind {
	case "movie":
		_, err = a.refreshMovie(ctx, it)
	case "show":
		_, err = a.refreshShow(ctx, it)
	case "season", "episode":
		show, e := a.st.Item(it.ShowID)
		if e != nil {
			return e
		}
		err = a.refreshChildren(ctx, show, false)
	}
	if errors.Is(err, ErrNoMatch) {
		return fmt.Errorf("no metadata match found for %q", it.Title)
	}
	return err
}

// Unmatch clears a manual match so the automatic matcher runs again.
func (a *Agent) Unmatch(itemID int64) error {
	it, err := a.st.Item(itemID)
	if err != nil {
		return err
	}
	it.ProviderIDs = map[string]string{}
	it.MetaLocked = false
	it.MetaStatus = store.MetaPending
	if err := a.st.SaveMetadata(it); err != nil {
		return err
	}
	a.Trigger()
	return nil
}
