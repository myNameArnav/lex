package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"lex/internal/store"
)

// withWatchlist marks the items on the user's watchlist (best effort: a
// failure only leaves the flags off).
func (s *Server) withWatchlist(uid int64, items ...*store.Item) {
	if err := s.St.AnnotateWatchlist(uid, items...); err != nil {
		s.Log.Warnf("watchlist: %v", err)
	}
}

func (s *Server) setWatchlist(w http.ResponseWriter, r *http.Request) {
	it, ok := s.pathItem(w, r)
	if !ok {
		return
	}
	id := it.ID
	var req struct{ Watchlist bool }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	switch err := s.St.SetWatchlist(userOf(r).ID, id, req.Watchlist); {
	case errors.Is(err, store.ErrNotWatchlistable):
		writeErr(w, 400, err.Error())
		return
	case err != nil:
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) watchlist(w http.ResponseWriter, r *http.Request) {
	items, err := s.St.Watchlist(userOf(r).ID, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"items": nonNilItems(items)})
}

// favorites lists the user's favorite movies and shows across the libraries
// they may see.
func (s *Server) favorites(w http.ResponseWriter, r *http.Request) {
	uid := userOf(r).ID
	items, err := s.St.Favorites(uid, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.withWatchlist(uid, items...)
	writeJSON(w, map[string]any{"items": nonNilItems(items)})
}

func (s *Server) collection(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	uid := userOf(r).ID
	c, items, err := s.St.CollectionItems(uid, id)
	// Only the libraries the user can see count: a collection with none of
	// its movies there doesn't exist for them.
	if u := userOf(r); err == nil && len(items) == 0 && !u.IsAdmin && !u.AllLibraries {
		err = store.ErrNotFound
	}
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	s.withWatchlist(uid, items...)
	writeJSON(w, map[string]any{"collection": c, "items": nonNilItems(items)})
}

// personImage points at a portrait of the person in some title's cast list
// (served by /api/items/{item}/cast/{index}/image).
type personImage struct {
	ItemID    int64 `json:"itemId"`
	UpdatedAt int64 `json:"updatedAt"`
	Index     int   `json:"index"`
}

// person lists the titles in the library a cast or crew member appears in,
// with the role they had in each.
func (s *Server) person(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, 400, "name is required")
		return
	}
	uid := userOf(r).ID
	items, err := s.St.PersonItems(uid, name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.withWatchlist(uid, items...)
	roles := map[string]string{}
	var image *personImage
	display := ""
	for _, it := range items {
		var rs []string
		for i, p := range it.Cast {
			if !strings.EqualFold(p.Name, name) {
				continue
			}
			if p.Role != "" {
				rs = append(rs, p.Role)
			}
			if image == nil && p.Image != "" {
				image = &personImage{ItemID: it.ID, UpdatedAt: it.UpdatedAt, Index: i}
			}
			if display == "" {
				display = p.Name // as the metadata spells it
			}
		}
		roles[strconv.FormatInt(it.ID, 10)] = strings.Join(rs, ", ")
	}
	if display == "" {
		display = name
	}
	writeJSON(w, map[string]any{"name": display, "image": image, "roles": roles, "items": nonNilItems(items)})
}
