package api

import (
	"net/http"
	"path/filepath"
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
	if libs == nil {
		libs = []store.Library{}
	}
	libs = libraryViews(libs, userOf(r).IsAdmin)
	writeJSON(w, libs)
}

func libraryViews(libs []store.Library, admin bool) []store.Library {
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
	uid := userOf(r).ID
	var rows []homeRow
	if cw, err := s.St.ContinueWatching(uid, 20); err == nil && len(cw) > 0 {
		rows = append(rows, homeRow{ID: "continue", Title: "Continue Watching", Kind: "landscape", Items: cw})
	}
	if nu, err := s.St.NextUp(uid, 20); err == nil && len(nu) > 0 {
		rows = append(rows, homeRow{ID: "nextup", Title: "Next Up", Kind: "landscape", Items: nu})
	}
	libs, _ := s.St.Libraries()
	for _, l := range libs {
		items, _, err := s.St.ListItems(uid, store.ListQuery{LibraryID: l.ID, Sort: "latest", Desc: true, Limit: 24})
		if err == nil && len(items) > 0 {
			rows = append(rows, homeRow{ID: "lib-" + strconv.FormatInt(l.ID, 10), Title: "Recently Added in " + l.Name, Kind: "poster", LibraryID: l.ID, Items: items})
		}
	}
	if rows == nil {
		rows = []homeRow{}
	}
	writeJSON(w, map[string]any{"rows": rows, "libraries": libraryViews(libs, userOf(r).IsAdmin)})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := store.ListQuery{
		Kind: q.Get("kind"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1" || q.Get("desc") == "true",
		Genre: q.Get("genre"), Filter: q.Get("filter"), Search: strings.TrimSpace(q.Get("q")),
		Limit: qInt(r, "limit", 100), Offset: qInt(r, "offset", 0),
	}
	lq.LibraryID, _ = strconv.ParseInt(q.Get("library"), 10, 64)
	items, total, err := s.St.ListItems(userOf(r).ID, lq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": nonNilItems(items), "total": total})
}

func (s *Server) genres(w http.ResponseWriter, r *http.Request) {
	lib, _ := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
	g, err := s.St.Genres(lib)
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
		fv.File = fileForUser(f, admin)
		out = append(out, fv)
	}
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
	out := map[string]any{"item": it, "segments": s.segments(it.ID)}
	switch it.Kind {
	case "movie", "episode":
		out["files"] = s.fileViews(it.ID, u.IsAdmin)
	case "show":
		ch, _ := s.St.Children(it.ID, u.ID)
		out["children"] = nonNilItems(ch)
	case "season":
		ch, _ := s.St.Children(it.ID, u.ID)
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
		eps, _ := s.St.ShowEpisodes(it.ShowID, u.ID)
		for i, e := range eps {
			if e.ID == it.ID {
				if i > 0 {
					out["prev"] = eps[i-1]
				}
				if i+1 < len(eps) {
					out["next"] = eps[i+1]
				}
			}
		}
	}
	if it.Kind == "show" {
		// "Play" on a show resumes the next unwatched episode.
		eps, _ := s.St.ShowEpisodes(it.ID, u.ID)
		var next *store.Item
		for _, e := range eps {
			if e.Season > 0 && e.UserData != nil && !e.UserData.Played {
				next = e
				break
			}
		}
		if next == nil && len(eps) > 0 {
			next = eps[0]
		}
		if next != nil {
			out["nextEpisode"] = next
		}
	}
	writeJSON(w, out)
}

func (s *Server) itemChildren(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	ch, err := s.St.Children(id, userOf(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, nonNilItems(ch))
}

func (s *Server) setPlayed(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
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

func (s *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct{ Favorite bool }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	if err := s.St.SetFavorite(userOf(r).ID, id, req.Favorite); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
