package api

import (
	"net/http"

	"lex/internal/intro"
)

func (s *Server) segments(itemID int64) []intro.Segment {
	return intro.Segments(s.St.DB(), itemID)
}

func (s *Server) cacheStatus(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Cache.Entries()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"status": s.Cache.Status(), "entries": entries})
}

// cacheItem queues every file of an item (movie/episode) or of all episodes
// of a season/show for caching.
func (s *Server) cacheItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	if !s.St.Config().CacheEnabled {
		writeErr(w, 400, "the SSD cache is disabled (Settings → Cache)")
		return
	}
	n := 0
	for _, it := range s.itemTree(id) {
		files, _ := s.St.ItemFiles(it)
		for _, f := range files {
			s.Cache.Request(f, "manual")
			n++
		}
	}
	writeJSON(w, map[string]int{"queued": n})
}

func (s *Server) uncacheItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	for _, it := range s.itemTree(id) {
		files, _ := s.St.ItemFiles(it)
		for _, f := range files {
			s.Cache.Remove(f.ID)
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// itemTree returns the item itself plus all episodes below a show/season.
func (s *Server) itemTree(id int64) []int64 {
	out := []int64{id}
	rows, err := s.St.DB().Query(`SELECT id FROM items WHERE kind='episode' AND (parent_id=? OR show_id=?)`, id, id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var x int64
		rows.Scan(&x)
		out = append(out, x)
	}
	return out
}

func (s *Server) introScan(w http.ResponseWriter, r *http.Request) {
	if !s.Intro.Available {
		writeErr(w, 400, "this ffmpeg has no chromaprint support")
		return
	}
	s.Intro.Trigger()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) introReset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	s.Intro.Reset(id)
	s.Intro.Trigger()
	writeJSON(w, map[string]bool{"ok": true})
}
