package library

import (
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

type Status struct {
	Running    bool   `json:"running"`
	Phase      string `json:"phase"`
	Library    string `json:"library"`
	Found      int    `json:"found"`
	Added      int    `json:"added"`
	Removed    int    `json:"removed"`
	Changed    int    `json:"changed"`
	ProbeTotal int    `json:"probeTotal"`
	ProbeDone  int    `json:"probeDone"`
	Current    string `json:"current"`
	StartedAt  int64  `json:"startedAt"`
	FinishedAt int64  `json:"finishedAt"`
	LastError  string `json:"lastError,omitempty"`
}

type Scanner struct {
	st      *store.Store
	ffprobe string
	log     *logx.Logger
	// OnDone is invoked after a scan+probe pass completes.
	OnDone func()
	// OnProbed is invoked after each file is probed.
	OnProbed func(f *store.File, info *store.MediaInfo)

	run    sync.Mutex // one scan at a time
	mu     sync.Mutex
	status Status
	queued bool
}

func NewScanner(st *store.Store, ffprobe string, log *logx.Logger) *Scanner {
	return &Scanner{st: st, ffprobe: ffprobe, log: log}
}

func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Scanner) update(fn func(*Status)) {
	s.mu.Lock()
	fn(&s.status)
	s.mu.Unlock()
}

// Trigger starts a background scan. If one is running, another pass is queued.
func (s *Scanner) Trigger(libID int64) {
	s.mu.Lock()
	if s.status.Running {
		s.queued = true
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	go s.ScanAll(context.Background(), libID)
}

func (s *Scanner) ScanAll(ctx context.Context, libID int64) {
	s.run.Lock()
	defer s.run.Unlock()
	for {
		s.update(func(st *Status) {
			*st = Status{Running: true, Phase: "scanning", StartedAt: time.Now().Unix()}
		})
		start := time.Now()
		libs, err := s.st.Libraries()
		if err != nil {
			s.log.Errorf("scan: %v", err)
		}
		for _, lib := range libs {
			if libID > 0 && lib.ID != libID {
				continue
			}
			s.update(func(st *Status) { st.Library = lib.Name })
			if err := s.scanLibrary(ctx, lib); err != nil {
				s.log.Errorf("scan library %q: %v", lib.Name, err)
				s.update(func(st *Status) { st.LastError = err.Error() })
			}
		}
		st := s.Status()
		s.log.Infof("scan finished in %s: found=%d added=%d changed=%d removed=%d", time.Since(start).Round(time.Millisecond), st.Found, st.Added, st.Changed, st.Removed)
		s.probeAll(ctx)
		s.update(func(st *Status) {
			st.Running = false
			st.Phase = "idle"
			st.Current = ""
			st.FinishedAt = time.Now().Unix()
		})
		if s.OnDone != nil {
			s.OnDone()
		}
		s.mu.Lock()
		again := s.queued
		s.queued = false
		s.mu.Unlock()
		if !again {
			return
		}
		libID = 0
	}
}

type entry struct {
	path  string
	root  string
	size  int64
	mtime int64
}

// target is the item a file should belong to.
type target struct {
	kind      string // movie | episode
	title     string
	year      int
	hint      store.Hint
	key       string
	showKey   string
	showTitle string
	showYear  int
	showHint  store.Hint
	season    int
	episode   int
	epEnd     int
}

func (s *Scanner) scanLibrary(ctx context.Context, lib store.Library) error {
	existing, err := s.st.LibraryFiles(lib.ID)
	if err != nil {
		return err
	}
	byPath := make(map[string]*store.File, len(existing))
	for _, f := range existing {
		byPath[f.Path] = f
	}
	var entries []entry
	okRoots := map[string]bool{}
	for _, root := range lib.Paths {
		root = filepath.Clean(root)
		fi, err := os.Stat(root)
		if err != nil || !fi.IsDir() {
			s.log.Warnf("library %q: path %s unavailable (%v); keeping existing items", lib.Name, root, err)
			continue
		}
		n := 0
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				s.log.Warnf("scan: %s: %v", p, err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			name := d.Name()
			if d.IsDir() {
				if p != root && (SkipDir(name) || strings.EqualFold(name, "bdmv") || strings.EqualFold(name, "video_ts")) {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") || !IsVideo(name) || ExtraSuffix(name) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if IsSample(name, info.Size()) {
				return nil
			}
			entries = append(entries, entry{path: p, root: root, size: info.Size(), mtime: info.ModTime().Unix()})
			n++
			return nil
		})
		if walkErr != nil {
			s.log.Warnf("library %q: walking %s failed: %v", lib.Name, root, walkErr)
			continue
		}
		// An empty root while we still have files there usually means an
		// unmounted share: don't wipe the library.
		if n == 0 {
			had := false
			for p := range byPath {
				if strings.HasPrefix(p, root+string(filepath.Separator)) {
					had = true
					break
				}
			}
			if had {
				s.log.Warnf("library %q: %s is empty; assuming it is unmounted and keeping items", lib.Name, root)
				continue
			}
		}
		okRoots[root] = true
	}
	s.update(func(st *Status) { st.Found += len(entries) })

	// Count videos per directory to decide between folder- and file-named movies.
	perDir := map[string]int{}
	for _, e := range entries {
		perDir[filepath.Dir(e.path)]++
	}
	hints := &hintCache{m: map[string]store.Hint{}}
	cache := map[string]int64{}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.path] = true
	}
	// Files that vanished, keyed by size+mtime: a new file with the same key
	// is the same file moved or renamed (e.g. by Sonarr/Radarr).
	type fkey struct{ size, mtime int64 }
	vanished := map[fkey][]*store.File{}
	for p, f := range byPath {
		if !seen[p] {
			k := fkey{f.Size, f.Mtime}
			vanished[k] = append(vanished[k], f)
		}
	}
	for _, e := range entries {
		t := classify(lib, e, perDir, hints)
		itemID, err := s.ensureItem(lib, t, cache)
		if err != nil {
			s.log.Errorf("scan: %s: %v", e.path, err)
			continue
		}
		if f := byPath[e.path]; f != nil {
			if f.Size != e.size || f.Mtime != e.mtime {
				s.st.MarkFileChanged(f.ID, e.size, e.mtime)
				s.update(func(st *Status) { st.Changed++ })
			}
			if f.ItemID != itemID {
				s.st.MoveFile(f.ID, itemID)
			}
			continue
		}
		if cands := vanished[fkey{e.size, e.mtime}]; len(cands) > 0 && e.size > 0 {
			old := cands[0]
			vanished[fkey{e.size, e.mtime}] = cands[1:]
			if err := s.st.RelocateFile(old.ID, e.path, old.ItemID, itemID); err == nil {
				s.log.Infof("scan: %s moved to %s", filepath.Base(old.Path), e.path)
				delete(byPath, old.Path)
				s.update(func(st *Status) { st.Changed++ })
				continue
			}
		}
		if _, err := s.st.InsertFile(&store.File{ItemID: itemID, LibraryID: lib.ID, Path: e.path, Size: e.size, Mtime: e.mtime}); err != nil {
			// Same path may exist in another library; skip.
			s.log.Warnf("scan: add %s: %v", e.path, err)
			continue
		}
		s.update(func(st *Status) { st.Added++ })
	}
	removed := 0
	for p, f := range byPath {
		if seen[p] {
			continue
		}
		inOkRoot := false
		for r := range okRoots {
			if strings.HasPrefix(p, r+string(filepath.Separator)) || p == r {
				inOkRoot = true
				break
			}
		}
		// Files outside any configured path (path removed from library) go too.
		inAnyRoot := false
		for _, r := range lib.Paths {
			if strings.HasPrefix(p, filepath.Clean(r)+string(filepath.Separator)) {
				inAnyRoot = true
			}
		}
		if inOkRoot || !inAnyRoot {
			s.st.DeleteFile(f.ID)
			removed++
		}
	}
	if _, err := s.st.PruneEmpty(lib.ID); err != nil {
		return err
	}
	s.update(func(st *Status) { st.Removed += removed })
	return s.st.SetLibraryScanned(lib.ID)
}

func (s *Scanner) ensureItem(lib store.Library, t target, cache map[string]int64) (int64, error) {
	get := func(kind, key string, mk func() *store.Item, refresh func(*store.Item)) (int64, error) {
		ck := kind + "\x00" + key
		if id, ok := cache[ck]; ok {
			return id, nil
		}
		it, err := s.st.FindItemByPath(lib.ID, kind, key)
		if err == nil {
			if refresh != nil {
				refresh(it)
			}
			cache[ck] = it.ID
			return it.ID, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		n := mk()
		n.LibraryID = lib.ID
		n.Kind = kind
		n.Path = key
		id, err := s.st.InsertItem(n)
		if err != nil {
			return 0, err
		}
		cache[ck] = id
		return id, nil
	}
	if t.kind == "movie" {
		return get("movie", t.key, func() *store.Item {
			return &store.Item{Title: t.title, Year: t.year, Hint: t.hint}
		}, func(it *store.Item) {
			if it.Hint != t.hint && !it.MetaLocked {
				s.st.SetItemStructure(it.ID, t.title, t.year, 0, 0, 0, t.hint)
				if it.MetaStatus == store.MetaNotFound {
					s.st.SetMetaStatus(it.ID, store.MetaPending)
				}
			}
		})
	}
	showID, err := get("show", t.showKey, func() *store.Item {
		return &store.Item{Title: t.showTitle, Year: t.showYear, Hint: t.showHint}
	}, func(it *store.Item) {
		if it.Hint != t.showHint && !it.MetaLocked {
			s.st.SetItemStructure(it.ID, t.showTitle, t.showYear, 0, 0, 0, t.showHint)
			if it.MetaStatus == store.MetaNotFound {
				s.st.SetMetaStatus(it.ID, store.MetaPending)
			}
		}
	})
	if err != nil {
		return 0, err
	}
	seasonKey := fmt.Sprintf("%s|S%d", t.showKey, t.season)
	seasonID, err := get("season", seasonKey, func() *store.Item {
		title := fmt.Sprintf("Season %d", t.season)
		if t.season == 0 {
			title = "Specials"
		}
		return &store.Item{Title: title, ParentID: showID, ShowID: showID, Season: t.season}
	}, nil)
	if err != nil {
		return 0, err
	}
	return get("episode", t.key, func() *store.Item {
		return &store.Item{Title: t.title, ParentID: seasonID, ShowID: showID, Season: t.season, Episode: t.episode, EpisodeEnd: t.epEnd}
	}, nil)
}

var reEpMarker = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s\d{1,3}[ ._-]*e\d{1,4}(?:[ ._-]*-?[ ._-]*e\d{1,4})*(?:-\d{1,4})?|\d{1,2}x\d{2,3})`)

func classify(lib store.Library, e entry, perDir map[string]int, hints *hintCache) target {
	rel, _ := filepath.Rel(e.root, e.path)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	dirs := parts[:len(parts)-1]
	base := parts[len(parts)-1]
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	dirSeason := -1
	for i := len(dirs) - 1; i >= 1; i-- { // dirs[0] is the show folder itself
		if s := SeasonFromDir(dirs[i]); s >= 0 {
			dirSeason = s
			break
		}
	}
	isEpisode := false
	switch lib.Kind {
	case "shows":
		isEpisode = true
	case "mixed":
		if dirSeason >= 0 {
			isEpisode = true
		} else if _, ok := ParseEpisode(base, -1, false); ok && reEpMarker.MatchString(stem) {
			isEpisode = true
		}
	}
	if !isEpisode {
		return classifyMovie(e, dirs, stem, perDir, hints)
	}

	t := target{kind: "episode"}
	if len(dirs) > 0 {
		showDir := filepath.Join(e.root, dirs[0])
		t.showKey = showDir
		t.showTitle, t.showYear = CleanTitle(dirs[0])
		t.showHint = hints.get(showDir, "show")
	} else {
		// Loose episode file in the library root: derive the show from the name.
		name := stem
		if loc := reEpMarker.FindStringIndex(stem); loc != nil && loc[0] > 0 {
			name = stem[:loc[0]]
		}
		t.showTitle, t.showYear = CleanTitle(name)
		t.showKey = e.root + "#" + Normalize(t.showTitle)
	}
	if t.showHint.Title != "" {
		t.showTitle = t.showHint.Title
	}
	if t.showHint.Year > 0 {
		t.showYear = t.showHint.Year
	}
	t.showHint.Title, t.showHint.Year = t.showTitle, t.showYear

	ei, ok := ParseEpisode(base, dirSeason, true)
	if ok {
		t.season, t.episode, t.epEnd = ei.Season, ei.Episode, ei.EpisodeEnd
		if dirSeason >= 0 && !ei.HasSeason {
			t.season = dirSeason
		}
		t.key = fmt.Sprintf("%s|S%dE%d", t.showKey, t.season, t.episode)
		t.title = episodeTitle(stem)
		if t.title == "" {
			t.title = fmt.Sprintf("Episode %d", t.episode)
		}
	} else {
		t.season = max(dirSeason, 0)
		t.key = e.path
		t.title, _ = CleanTitle(stem)
	}
	return t
}

// episodeTitle pulls "Pilot" out of "Show - S01E01 - Pilot HDTV-1080p".
func episodeTitle(stem string) string {
	loc := reEpMarker.FindStringIndex(stem)
	if loc == nil {
		return ""
	}
	rest := strings.TrimLeft(stem[loc[1]:], " -._")
	if rest == "" {
		return ""
	}
	norm := normalizeSeparators(rest)
	if l := reJunk.FindStringIndex(norm); l != nil {
		norm = norm[:l[0]]
	}
	norm = reBrackets.ReplaceAllString(norm, " ")
	norm = strings.Trim(reSpaces.ReplaceAllString(norm, " "), " -_.")
	return norm
}

func classifyMovie(e entry, dirs []string, stem string, perDir map[string]int, hints *hintCache) target {
	t := target{kind: "movie"}
	dir := filepath.Dir(e.path)
	fileTitle, fileYear := CleanTitle(stem)
	if len(dirs) > 0 && perDir[dir] == 1 {
		dirName := dirs[len(dirs)-1]
		dt, dy := CleanTitle(dirName)
		t.title, t.year = dt, dy
		if dy == 0 && fileYear > 0 {
			t.title, t.year = fileTitle, fileYear
		}
		t.key = dir
		t.hint = hints.get(dir, "movie")
	} else {
		t.title, t.year = fileTitle, fileYear
		t.key = dir + "#" + Normalize(fileTitle) + strconv.Itoa(fileYear)
		t.hint = hints.getFile(e.path)
	}
	if t.hint.Title != "" {
		t.title = t.hint.Title
	}
	if t.hint.Year > 0 {
		t.year = t.hint.Year
	}
	t.hint.Title, t.hint.Year = t.title, t.year
	return t
}

// ---- sidecar hints (.plexmatch, NFO) ----

type hintCache struct{ m map[string]store.Hint }

func (h *hintCache) get(dir, kind string) store.Hint {
	if v, ok := h.m[dir]; ok {
		return v
	}
	var hint store.Hint
	readPlexMatch(filepath.Join(dir, ".plexmatch"), &hint)
	nfo := "movie.nfo"
	if kind == "show" {
		nfo = "tvshow.nfo"
	}
	readNFO(filepath.Join(dir, nfo), &hint)
	h.m[dir] = hint
	return hint
}

func (h *hintCache) getFile(p string) store.Hint {
	var hint store.Hint
	readNFO(strings.TrimSuffix(p, filepath.Ext(p))+".nfo", &hint)
	return hint
}

func readPlexMatch(p string, h *store.Hint) {
	f, err := os.Open(p)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "title":
			h.Title = v
		case "year":
			h.Year, _ = strconv.Atoi(v)
		case "tmdbid":
			h.TMDB = v
		case "tvdbid":
			h.TVDB = v
		case "imdbid":
			h.IMDB = v
		case "guid":
			// e.g. tmdb://12345, tvdb://1234, imdb://tt123
			if a, b, ok := strings.Cut(v, "://"); ok {
				switch a {
				case "tmdb":
					h.TMDB = b
				case "tvdb":
					h.TVDB = b
				case "imdb":
					h.IMDB = b
				}
			}
		}
	}
}

type nfoDoc struct {
	Title     string `xml:"title"`
	Year      int    `xml:"year"`
	TMDBID    string `xml:"tmdbid"`
	IMDBID    string `xml:"imdbid"`
	TVDBID    string `xml:"tvdbid"`
	ID        string `xml:"id"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
}

func readNFO(p string, h *store.Hint) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 1<<20 {
		return
	}
	var d nfoDoc
	if xml.Unmarshal(b, &d) != nil {
		// NFOs are sometimes just a URL; pull ids out of it.
		s := string(b)
		if m := regexp.MustCompile(`tt\d{6,10}`).FindString(s); m != "" && h.IMDB == "" {
			h.IMDB = m
		}
		if m := regexp.MustCompile(`themoviedb\.org/(?:movie|tv)/(\d+)`).FindStringSubmatch(s); m != nil && h.TMDB == "" {
			h.TMDB = m[1]
		}
		return
	}
	if d.Title != "" && h.Title == "" {
		h.Title = strings.TrimSpace(d.Title)
	}
	if d.Year > 0 && h.Year == 0 {
		h.Year = d.Year
	}
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" && *dst == "" {
			*dst = v
		}
	}
	set(&h.TMDB, d.TMDBID)
	set(&h.IMDB, d.IMDBID)
	set(&h.TVDB, d.TVDBID)
	for _, u := range d.UniqueIDs {
		switch strings.ToLower(u.Type) {
		case "tmdb":
			set(&h.TMDB, u.Value)
		case "imdb":
			set(&h.IMDB, u.Value)
		case "tvdb":
			set(&h.TVDB, u.Value)
		}
	}
	if strings.HasPrefix(d.ID, "tt") {
		set(&h.IMDB, d.ID)
	}
}

// ---- probing ----

func (s *Scanner) probeAll(ctx context.Context) {
	files, err := s.st.FilesNeedingProbe()
	if err != nil {
		s.log.Errorf("probe: %v", err)
		return
	}
	if len(files) == 0 {
		return
	}
	workers := s.st.Config().ProbeWorkers
	s.update(func(st *Status) { st.Phase = "probing"; st.ProbeTotal = len(files); st.ProbeDone = 0 })
	s.log.Infof("probing %d files with %d worker(s)", len(files), workers)
	ch := make(chan *store.File)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range ch {
				s.update(func(st *Status) { st.Current = filepath.Base(f.Path) })
				info, err := s.ProbeFile(ctx, f.Path)
				if err != nil {
					s.log.Warnf("probe %s: %v", f.Path, err)
					s.st.SaveProbe(f.ID, nil, err.Error())
				} else {
					s.st.SaveProbe(f.ID, info, "")
					if s.OnProbed != nil {
						s.OnProbed(f, info)
					}
				}
				s.update(func(st *Status) { st.ProbeDone++ })
			}
		}()
	}
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		ch <- f
	}
	close(ch)
	wg.Wait()
}

// ProbeFile runs ffprobe at reduced priority.
func (s *Scanner) ProbeFile(ctx context.Context, path string) (*store.MediaInfo, error) {
	return Probe(ctx, s.ffprobe, path)
}
