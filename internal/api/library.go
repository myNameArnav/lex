package api

import (
	"cmp"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"lex/internal/library"
	"lex/internal/store"
)

func (s *Server) libraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.St.Libraries()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	u := userOf(r)
	writeJSON(w, libraryViews(visibleLibraries(libs, u), u.IsAdmin))
}

// adminLibrary adds what the settings page shows about a library's folders.
type adminLibrary struct {
	store.Library
	// Elsewhere counts the files in this library's folders that another
	// library has (a file can only be in one): a library nested in another's
	// folder stays empty.
	Elsewhere []libraryShare `json:"elsewhere,omitempty"`
}

type libraryShare struct {
	Library string `json:"library"`
	Files   int    `json:"files"`
}

func (s *Server) adminLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.St.Libraries()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]adminLibrary, 0, len(libs))
	for _, l := range libs {
		al := adminLibrary{Library: l}
		others, err := s.St.OtherLibraryFiles(l.ID, l.Paths)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		counts := map[string]int{}
		for _, name := range others {
			counts[name]++
		}
		for _, o := range libs {
			if n := counts[o.Name]; n > 0 {
				al.Elsewhere = append(al.Elsewhere, libraryShare{Library: o.Name, Files: n})
				delete(counts, o.Name)
			}
		}
		out = append(out, al)
	}
	writeJSON(w, out)
}

// libraryViews hides folder paths from non-admins and never returns nil, so
// clients always get a JSON array.
func libraryViews(libs []store.Library, admin bool) []store.Library {
	if libs == nil {
		return []store.Library{}
	}
	if !admin {
		libs = append([]store.Library{}, libs...)
		for i := range libs {
			libs[i].Paths = []string{}
		}
	}
	return libs
}

func fileForUser(f *store.File, admin bool) *store.File {
	if admin {
		return f
	}
	c := *f
	c.Path, c.ProbeError = "", ""
	return &c
}

func nonNilItems(v []*store.Item) []*store.Item {
	if v == nil {
		return []*store.Item{}
	}
	return v
}

type homeRow struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Kind      string        `json:"kind"` // landscape | poster
	LibraryID int64         `json:"libraryId,omitempty"`
	Items     []*store.Item `json:"items"`
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	uid := u.ID
	var rows []homeRow
	if cw, err := s.St.ContinueWatching(uid, 20); err == nil && len(cw) > 0 {
		rows = append(rows, homeRow{ID: "continue", Title: "Continue Watching", Kind: "landscape", Items: cw})
	}
	if nu, err := s.St.NextUp(uid, 20); err == nil && len(nu) > 0 {
		rows = append(rows, homeRow{ID: "nextup", Title: "Next Up", Kind: "landscape", Items: nu})
	}
	if wl, err := s.St.Watchlist(uid, 24); err == nil && len(wl) > 0 {
		rows = append(rows, homeRow{ID: "watchlist", Title: "Watchlist", Kind: "poster", Items: wl})
	}
	if fav, err := s.St.Favorites(uid, 24); err == nil && len(fav) > 0 {
		rows = append(rows, homeRow{ID: "favorites", Title: "Favorites", Kind: "poster", Items: fav})
	}
	libs, _ := s.St.Libraries()
	libs = visibleLibraries(libs, u)
	for _, l := range libs {
		items, _, err := s.St.ListItems(uid, store.ListQuery{LibraryID: l.ID, Sort: "latest", Desc: true, Limit: 24})
		if err == nil && len(items) > 0 {
			rows = append(rows, homeRow{ID: "lib-" + strconv.FormatInt(l.ID, 10), Title: "Recently Added in " + l.Name, Kind: "poster", LibraryID: l.ID, Items: items})
		}
	}
	if rows == nil {
		rows = []homeRow{}
	}
	for _, row := range rows {
		if row.ID != "watchlist" {
			s.withWatchlist(uid, row.Items...)
		}
	}
	writeJSON(w, map[string]any{"rows": rows, "libraries": libraryViews(libs, u.IsAdmin)})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := store.ListQuery{
		Kind: q.Get("kind"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1" || q.Get("desc") == "true",
		Genre: q.Get("genre"), Filter: q.Get("filter"), Search: strings.TrimSpace(q.Get("q")),
		Limit: qInt(r, "limit", 100), Offset: qInt(r, "offset", 0),
	}
	lq.LibraryID, _ = strconv.ParseInt(q.Get("library"), 10, 64)
	// An unparsable (e.g. oversized) seed means unseeded, not a fixed order.
	if seed, err := strconv.ParseInt(q.Get("seed"), 10, 64); err == nil {
		lq.Seed = seed
	}
	items, total, err := s.St.ListItems(userOf(r).ID, lq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.withWatchlist(userOf(r).ID, items...)
	writeJSON(w, map[string]any{"items": nonNilItems(items), "total": total})
}

// letters counts the titles of a title-sorted /api/items listing by first
// letter (store.LetterCounts), for the library grid's A–Z rail. It takes
// the same library, kind, genre and filter parameters.
func (s *Server) letters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := store.ListQuery{Kind: q.Get("kind"), Genre: q.Get("genre"), Filter: q.Get("filter")}
	lq.LibraryID, _ = strconv.ParseInt(q.Get("library"), 10, 64)
	counts, err := s.St.LetterCounts(userOf(r).ID, lq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, counts)
}

func (s *Server) genres(w http.ResponseWriter, r *http.Request) {
	lib, _ := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
	g, err := s.St.Genres(userOf(r).ID, lib)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if g == nil {
		g = []string{}
	}
	writeJSON(w, g)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	uid := userOf(r).ID
	out := map[string][]*store.Item{"movies": {}, "shows": {}, "episodes": {}}
	if q == "" {
		writeJSON(w, out)
		return
	}
	if items, _, err := s.St.ListItems(uid, store.ListQuery{Kind: "movie", Search: q, Limit: 40}); err == nil {
		out["movies"] = nonNilItems(items)
	}
	if items, _, err := s.St.ListItems(uid, store.ListQuery{Kind: "show", Search: q, Limit: 40}); err == nil {
		out["shows"] = nonNilItems(items)
	}
	s.withWatchlist(uid, append(slices.Clone(out["movies"]), out["shows"]...)...)
	if items, _, err := s.St.ListItems(uid, store.ListQuery{Kind: "episode", Search: q, Sort: "premiere", Limit: 40}); err == nil {
		out["episodes"] = nonNilItems(items)
	}
	writeJSON(w, out)
}

type fileView struct {
	*store.File
	Name      string         `json:"name"`
	Subtitles []store.Stream `json:"subtitles"`
	Cached    bool           `json:"cached"`
}

func (s *Server) fileViews(itemID int64, admin bool) []fileView {
	files, _ := s.St.ItemFiles(itemID)
	return s.viewsOf(files, admin)
}

func (s *Server) viewsOf(files []*store.File, admin bool) []fileView {
	out := []fileView{}
	for _, f := range files {
		fv := fileView{File: f, Name: filepath.Base(f.Path), Subtitles: []store.Stream{}, Cached: s.Cache.IsCached(f.ID)}
		if f.Info != nil {
			for _, st := range f.Info.Streams {
				if st.Type == "subtitle" {
					fv.Subtitles = append(fv.Subtitles, st)
				}
			}
		}
		fv.Subtitles = append(fv.Subtitles, library.ExternalSubs(f.Path)...)
		fv.Subtitles = append(fv.Subtitles, s.downloadedStreams(f.ID)...)
		fv.File = fileForUser(f, admin)
		out = append(out, fv)
	}
	return out
}

// extraView is a local extra on a title's page.
type extraView struct {
	ID       int64   `json:"id"` // the file id, played with the title's id
	Type     string  `json:"type"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration"`
}

// extraLabels name the extra types where a file name gives no title.
var extraLabels = map[string]string{
	library.ExtraTrailer: "Trailer", library.ExtraFeaturette: "Featurette", library.ExtraBehindTheScenes: "Behind the scenes",
	library.ExtraDeleted: "Deleted scene", library.ExtraInterview: "Interview", library.ExtraScene: "Scene",
	library.ExtraShort: "Short", library.ExtraOther: "Extra",
}

// extraName is what an extra is called: its cleaned file name, or its type.
func extraName(f *store.File) string {
	if t := library.ExtraTitle(filepath.Base(f.Path)); t != "" {
		return t
	}
	if l := extraLabels[f.Extra]; l != "" {
		return l
	}
	return "Extra"
}

// extraViews lists a movie's or show's local extras, trailers first.
func (s *Server) extraViews(itemID int64) []extraView {
	files, _ := s.St.ItemExtras(itemID)
	out := []extraView{}
	for _, f := range files {
		out = append(out, extraView{ID: f.ID, Type: f.Extra, Title: extraName(f), Duration: f.Duration})
	}
	rank := func(e extraView) int {
		if e.Type == library.ExtraTrailer {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(a, b extraView) int { return cmp.Compare(rank(a), rank(b)) })
	return out
}

func (s *Server) itemDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	u := userOf(r)
	it, err := s.St.ItemForUser(id, u.ID)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	s.withWatchlist(u.ID, it)
	// ?extra=<file id>: what the player needs to play one of the title's
	// local extras, in place of its own files and episodes.
	if x := qInt(r, "extra", 0); x > 0 {
		extras, _ := s.St.ItemExtras(it.ID)
		for _, f := range extras {
			if int(f.ID) == x {
				writeJSON(w, map[string]any{"item": it, "segments": []any{}, "files": s.viewsOf([]*store.File{f}, u.IsAdmin),
					"extra": extraView{ID: f.ID, Type: f.Extra, Title: extraName(f), Duration: f.Duration}})
				return
			}
		}
		writeErr(w, 404, "no such extra")
		return
	}
	out := map[string]any{"item": it, "segments": s.segments(it.ID)}
	if it.Kind == "movie" {
		if c, err := s.St.ItemCollection(it.ID); err == nil {
			out["collection"] = c
		}
	}
	switch it.Kind {
	case "movie", "episode":
		out["files"] = s.fileViews(it.ID, u.IsAdmin)
	case "show", "season":
		ch, _ := s.St.ChildCards(it.ID, u.ID)
		out["children"] = nonNilItems(ch)
	}
	if it.ShowID > 0 {
		if show, err := s.St.ItemForUser(it.ShowID, u.ID); err == nil {
			out["show"] = show
		}
	}
	if it.Kind == "episode" {
		if season, err := s.St.Item(it.ParentID); err == nil {
			out["season"] = season
		}
		if prev, next, err := s.St.AdjacentEpisodes(it, u.ID); err == nil {
			if prev != nil {
				out["prev"] = prev
			}
			if next != nil {
				out["next"] = next
			}
		}
	}
	if it.Kind == "movie" || it.Kind == "show" {
		if ex := s.extraViews(it.ID); len(ex) > 0 {
			out["extras"] = ex
		}
	}
	if it.Kind == "show" {
		// "Play" on a show resumes the next unwatched episode.
		if next, err := s.St.ShowNextEpisode(it.ID, u.ID); err == nil && next != nil {
			out["nextEpisode"] = next
		}
	}
	writeJSON(w, out)
}

func (s *Server) itemChildren(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	ch, err := s.St.ChildCards(it.ID, userOf(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, nonNilItems(ch))
}

func (s *Server) setPlayed(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	id := it.ID
	var req struct{ Played bool }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if err := s.St.MarkTreePlayed(userOf(r).ID, id, req.Played); err != nil {
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// clearProgress removes the item from Continue Watching by dropping its
// resume point; a watched title stays watched.
func (s *Server) clearProgress(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	if err := s.St.ClearProgress(userOf(r).ID, it.ID); err != nil {
		writeErr(w, 500, "could not clear progress")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	id := it.ID
	var req struct{ Favorite bool }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if err := s.St.SetFavorite(userOf(r).ID, id, req.Favorite); err != nil {
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// shuffle returns a show's or a season's episodes in random order. A show
// leaves out its specials, unless that's all it has.
func (s *Server) shuffle(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	id, uid := it.ID, userOf(r).ID
	var err error
	var eps []*store.Item
	switch it.Kind {
	case "show":
		eps, err = s.St.ShowEpisodes(id, uid)
		if regular := slices.DeleteFunc(slices.Clone(eps), func(e *store.Item) bool { return e.Season == 0 }); len(regular) > 0 {
			eps = regular
		}
	case "season":
		eps, err = s.St.Children(id, uid)
	default:
		writeErr(w, 400, "only shows and seasons can be shuffled")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	rand.Shuffle(len(eps), func(i, j int) { eps[i], eps[j] = eps[j], eps[i] })
	writeJSON(w, nonNilItems(eps))
}
