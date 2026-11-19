// Package srv is a small API for the generator's tests.
package srv

import (
	"encoding/json"
	"net/http"
	"time"
)

type Server struct{}

type Base struct {
	ID      int64     `json:"id"`
	Created time.Time `json:"created"`
}

type Item struct {
	Base
	Title   string            `json:"title"`
	Year    int               `json:"year,omitempty"`
	Secret  string            `json:"-"`
	Parent  *Item             `json:"parent"`
	Raw     []byte            `json:"raw"`
	Tags    map[string]string `json:"tags"`
	Count   int64             `json:"count,string"`
	NoTag   bool
	private int
}

type Other struct {
	Item Item `json:"item"`
}

type itemReq struct {
	Title string `json:"title"`
}

func writeJSON(w http.ResponseWriter, v any)               { json.NewEncoder(w).Encode(v) }
func readJSON(r *http.Request, v any) error                { return json.NewDecoder(r.Body).Decode(v) }
func writeErr(w http.ResponseWriter, code int, msg string) {}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.Handler { return h }
	mux.Handle("GET /api/items/{id}", wrap(s.item))
	mux.HandleFunc("PUT /api/items/{id}", s.updateItem)
	mux.HandleFunc("GET /api/items/{id}/children", s.children)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"name": "x", "uptime": 1.5})
	})
	mux.HandleFunc("GET /api/image", s.image)
	mux.HandleFunc("GET /api/{path...}", s.image)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })
	return mux
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("short") {
		writeJSON(w, map[string]any{"item": Item{}, "short": true})
		return
	}
	out := map[string]any{"item": &Item{}}
	if r.URL.Query().Has("other") {
		out["other"] = Other{}
	}
	writeJSON(w, out)
}

func (s *Server) updateItem(w http.ResponseWriter, r *http.Request) {
	var req itemReq
	if readJSON(r, &req) != nil {
		writeErr(w, 400, "bad request")
		return
	}
	s.respond(w, []Item{})
}

func (s *Server) respond(w http.ResponseWriter, items []Item) { writeJSON(w, items) }

func (s *Server) children(w http.ResponseWriter, r *http.Request) {
	m := map[string]any{}
	for _, k := range []string{"a"} {
		m[k] = 1
	}
	writeJSON(w, m)
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) { w.Write(nil) }
