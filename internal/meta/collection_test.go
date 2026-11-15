package meta

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

// A movie matched through Radarr records the collection Radarr reports
// (v4+ "title", v3 "name"); with no metadata source nothing is fetched and
// no collection is kept.
func TestMovieRefreshRecordsCollection(t *testing.T) {
	movies := map[string]map[string]any{
		"11": {"id": 1, "title": "Hope", "year": 1977, "tmdbId": 11, "collection": map[string]any{"title": "Star Wars Collection", "tmdbId": 10}},
		"12": {"id": 2, "title": "Empire", "year": 1980, "tmdbId": 12, "collection": map[string]any{"name": "Star Wars Collection", "tmdbId": 10}},
		"13": {"id": 3, "title": "Heat", "year": 1995, "tmdbId": 13},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]any{})
		case "/api/v3/movie/lookup/tmdb":
			json.NewEncoder(w).Encode(movies[r.URL.Query().Get("tmdbId")])
		default:
			json.NewEncoder(w).Encode([]any{})
		}
	}))
	defer srv.Close()

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := st.Config()
	cfg.EnableTMDB, cfg.RadarrURL, cfg.RadarrKey = false, srv.URL, "key"
	if _, err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	lib, _ := st.CreateLibrary("Movies", "movies", []string{"/m"})
	a := NewAgent(st, NewImageCache(filepath.Join(t.TempDir(), "images")), logx.New(50, false))
	ids := map[string]int64{}
	for _, tmdb := range []string{"11", "12", "13"} {
		id, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "x" + tmdb, Path: "/m/" + tmdb, Hint: store.Hint{TMDB: tmdb}})
		ids[tmdb] = id
		if err := a.Refresh(context.Background(), id); err != nil {
			t.Fatalf("refresh %s: %v", tmdb, err)
		}
	}
	for _, tmdb := range []string{"11", "12"} {
		c, err := st.ItemCollection(ids[tmdb])
		if err != nil || *c != (store.Collection{ID: 10, Name: "Star Wars Collection"}) {
			t.Fatalf("movie %s: collection %+v, %v", tmdb, c, err)
		}
	}
	if _, err := st.ItemCollection(ids["13"]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Heat has a collection: %v", err)
	}

	// No source configured: the movie isn't matched and its stale
	// collection goes, without any request.
	cfg.RadarrURL, cfg.RadarrKey = "", ""
	st.SaveConfig(cfg)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Errorf("unexpected request %s", r.URL) })
	a.Refresh(context.Background(), ids["11"])
	if _, err := st.ItemCollection(ids["11"]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("collection kept without a source: %v", err)
	}
}
