package api

import (
	"net/http"
	"strconv"
	"strings"

	"lex/internal/trickplay"
)

func (s *Server) trickMeta(w http.ResponseWriter, r *http.Request) {
	f, ok := s.pathFile(w, r)
	if !ok {
		return
	}
	m, _, err := s.Trick.Meta(f)
	if err != nil {
		writeErr(w, 404, "no previews yet")
		return
	}
	writeJSON(w, m)
}

func (s *Server) trickSheet(w http.ResponseWriter, r *http.Request) {
	f, ok := s.pathFile(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("n"), ".jpg"))
	if err != nil || n < 1 {
		writeErr(w, 400, "bad sheet")
		return
	}
	m, dir, err := s.Trick.Meta(f)
	if err != nil || n > m.Sheets {
		writeErr(w, 404, "no such sheet")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=2592000, immutable")
	http.ServeFile(w, r, trickplay.Sheet(dir, n))
}
