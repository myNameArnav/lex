package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lex/internal/store"
)

func TestItemDetailNeighbours(t *testing.T) {
	s, h := cacheServer(t)
	token := tvLibrary(t, s)
	type detail struct {
		Prev, Next, NextEpisode *store.Item
		Children                []*store.Item
	}
	id := func(it *store.Item) int64 {
		if it == nil {
			return 0
		}
		return it.ID
	}
	// Show 2: special 5 (season 0), then 6, 7, 8 in season 1.
	for _, tc := range []struct{ ep, prev, next int64 }{{5, 0, 6}, {6, 5, 7}, {8, 7, 0}} {
		var d detail
		if code := getJSON(t, h, token, "/api/items/"+itoa(tc.ep), &d); code != 200 {
			t.Fatalf("episode %d: %d", tc.ep, code)
		}
		if id(d.Prev) != tc.prev || id(d.Next) != tc.next {
			t.Errorf("episode %d: prev/next = %d/%d, want %d/%d", tc.ep, id(d.Prev), id(d.Next), tc.prev, tc.next)
		}
	}
	var d detail
	getJSON(t, h, token, "/api/items/2", &d)
	if id(d.NextEpisode) != 6 || len(d.Children) != 2 {
		t.Fatalf("show: next episode %d, %d seasons", id(d.NextEpisode), len(d.Children))
	}
	u, _ := s.St.TokenUser(token)
	for _, ep := range []int64{6, 7, 8} {
		s.St.MarkPlayed(u.ID, ep, true)
	}
	// All watched: start over from the first episode, the special.
	getJSON(t, h, token, "/api/items/2", &d)
	if id(d.NextEpisode) != 5 {
		t.Fatalf("watched show: next episode %d, want 5", id(d.NextEpisode))
	}
}

func TestListResponsesAreCards(t *testing.T) {
	s, h := cacheServer(t)
	token := tvLibrary(t, s)
	if _, err := s.St.DB().Exec(`UPDATE items SET overview='Plot',cast_json='[{"name":"Al Pacino"}]',studios='["Warner"]',provider_ids='{"tmdb":"949"}'`); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		r := httptest.NewRequest("GET", "http://lex.test"+path, nil)
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		return w.Body.String()
	}
	for _, path := range []string{"/api/items?library=1", "/api/home", "/api/search?q=heat", "/api/search?q=pacino"} {
		body := get(path)
		if !strings.Contains(body, `"title":"Heat"`) {
			t.Errorf("%s: no Heat in %s", path, body)
		}
		for _, field := range []string{`"cast"`, `"studios"`, `"providerIds"`, `"overview"`} {
			if strings.Contains(body, field) {
				t.Errorf("%s has %s: %s", path, field, body)
			}
		}
	}
	// Episode rows keep their synopsis; the detail keeps everything.
	if body := get("/api/items/4/children"); !strings.Contains(body, `"overview":"Plot"`) || strings.Contains(body, `"cast"`) {
		t.Errorf("episode rows: %s", body)
	}
	if body := get("/api/items/1"); !strings.Contains(body, `"cast"`) || !strings.Contains(body, `"providerIds"`) {
		t.Errorf("detail: %s", body)
	}
}
