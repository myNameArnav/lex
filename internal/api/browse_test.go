package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lex/internal/store"
)

type browseClient struct {
	t     *testing.T
	h     http.Handler
	token string
}

func (c browseClient) do(method, path, body string) (int, map[string]any) {
	c.t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "http://lex.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, "http://lex.test"+path, nil)
	}
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: c.token})
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func ids(v any) []int64 {
	var out []int64
	list, _ := v.([]any)
	for _, x := range list {
		out = append(out, int64(x.(map[string]any)["id"].(float64)))
	}
	return out
}

func browseServer(t *testing.T) (*Server, browseClient, map[string]int64) {
	t.Helper()
	s, h := securityServer(t)
	u, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(u.ID, "Test", "127.0.0.1")
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{"/m"})
	tv, _ := s.St.CreateLibrary("TV", "shows", []string{"/tv"})
	got := map[string]int64{}
	add := func(key string, it *store.Item) {
		id, err := s.St.InsertItem(it)
		if err != nil {
			t.Fatal(err)
		}
		if it.Cast != nil || it.Premiere != "" {
			it.ID = id
			if err := s.St.SaveMetadata(it); err != nil {
				t.Fatal(err)
			}
		}
		got[key] = id
	}
	add("first", &store.Item{LibraryID: lib.ID, Kind: "movie", Title: "First", Year: 2001, Premiere: "2001-06-01", Path: "/m/1",
		Cast: []store.Person{{Name: "Ann Actor", Role: "Hero", Image: "url:https://image.tmdb.org/t/p/w185/a.jpg"}}})
	add("second", &store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Second", Year: 2004, Premiere: "2004-06-01", Path: "/m/2",
		Cast: []store.Person{{Name: "Dee Rector", Role: "Director"}, {Name: "ann actor", Role: "Hero"}}})
	add("show", &store.Item{LibraryID: tv.ID, Kind: "show", Title: "Show", Path: "/tv/show"})
	add("ep", &store.Item{LibraryID: tv.ID, Kind: "episode", ParentID: got["show"], ShowID: got["show"], Title: "Ep", Path: "/tv/show/e1"})
	return s, browseClient{t, h, token}, got
}

func TestWatchlistEndpoints(t *testing.T) {
	_, c, id := browseServer(t)
	path := func(k string) string { return "/api/items/" + itoa(id[k]) + "/watchlist" }
	for _, k := range []string{"first", "show"} {
		if code, out := c.do("POST", path(k), `{"watchlist":true}`); code != 200 {
			t.Fatalf("add %s: %d %v", k, code, out)
		}
	}
	if code, _ := c.do("POST", path("ep"), `{"watchlist":true}`); code != 400 {
		t.Fatalf("episode: %d, want 400", code)
	}
	if code, _ := c.do("POST", "/api/items/9999/watchlist", `{"watchlist":true}`); code != 404 {
		t.Fatalf("missing item: %d, want 404", code)
	}

	_, home := c.do("GET", "/api/home", "")
	var row map[string]any
	for _, r := range home["rows"].([]any) {
		if r.(map[string]any)["id"] == "watchlist" {
			row = r.(map[string]any)
		}
	}
	if row == nil || len(ids(row["items"])) != 2 {
		t.Fatalf("home watchlist row = %v", row)
	}

	_, list := c.do("GET", "/api/items?filter=watchlist&kind=movie", "")
	if got := ids(list["items"]); len(got) != 1 || got[0] != id["first"] {
		t.Fatalf("filtered list = %v", got)
	}
	_, all := c.do("GET", "/api/items?kind=movie", "")
	for _, x := range all["items"].([]any) {
		it := x.(map[string]any)
		ud, _ := it["userData"].(map[string]any)
		if on := ud["watchlist"] == true; on != (int64(it["id"].(float64)) == id["first"]) {
			t.Fatalf("list flags: %v", it)
		}
	}
	_, detail := c.do("GET", "/api/items/"+itoa(id["first"]), "")
	if ud := detail["item"].(map[string]any)["userData"].(map[string]any); ud["watchlist"] != true {
		t.Fatalf("detail userData = %v", ud)
	}

	c.do("POST", path("first"), `{"watchlist":false}`)
	if _, wl := c.do("GET", "/api/watchlist", ""); len(ids(wl["items"])) != 1 {
		t.Fatalf("after removal = %v", wl)
	}
}

func TestFavoritesEndpoint(t *testing.T) {
	s, c, id := browseServer(t)
	homeRow := func() map[string]any {
		t.Helper()
		_, home := c.do("GET", "/api/home", "")
		for _, r := range home["rows"].([]any) {
			if r.(map[string]any)["id"] == "favorites" {
				return r.(map[string]any)
			}
		}
		return nil
	}
	if row := homeRow(); row != nil {
		t.Fatalf("home favorites row with no favorites = %v", row)
	}
	if code, out := c.do("GET", "/api/favorites", ""); code != 200 || out["items"] == nil || len(ids(out["items"])) != 0 {
		t.Fatalf("empty favorites: %d %v", code, out)
	}
	for _, k := range []string{"first", "show"} {
		if code, out := c.do("POST", "/api/items/"+itoa(id[k])+"/favorite", `{"favorite":true}`); code != 200 {
			t.Fatalf("favorite %s: %d %v", k, code, out)
		}
	}
	c.do("POST", "/api/items/"+itoa(id["show"])+"/watchlist", `{"watchlist":true}`)
	// Newest first; both landed in the same second, so make "first" newer.
	s.St.DB().Exec(`UPDATE user_data SET favorited_at=favorited_at+10 WHERE item_id=?`, id["first"])

	_, favs := c.do("GET", "/api/favorites", "")
	if !equalIDs(ids(favs["items"]), id["first"], id["show"]) {
		t.Fatalf("favorites = %v", favs)
	}
	for _, x := range favs["items"].([]any) {
		it := x.(map[string]any)
		ud := it["userData"].(map[string]any)
		if ud["favorite"] != true || (ud["watchlist"] == true) != (int64(it["id"].(float64)) == id["show"]) {
			t.Fatalf("favorites userData = %v", it)
		}
	}
	if row := homeRow(); row == nil || !equalIDs(ids(row["items"]), id["first"], id["show"]) {
		t.Fatalf("home favorites row = %v", row)
	}
	c.do("POST", "/api/items/"+itoa(id["first"])+"/favorite", `{"favorite":false}`)
	if _, favs := c.do("GET", "/api/favorites", ""); !equalIDs(ids(favs["items"]), id["show"]) {
		t.Fatalf("after removal = %v", favs)
	}
}

func TestCollectionEndpoint(t *testing.T) {
	s, c, id := browseServer(t)
	col := &store.Collection{ID: 555, Name: "Saga"}
	s.St.SetItemCollection(id["second"], col)
	s.St.SetItemCollection(id["first"], col)

	_, detail := c.do("GET", "/api/items/"+itoa(id["first"]), "")
	if got, _ := detail["collection"].(map[string]any); got["name"] != "Saga" || got["id"] != float64(555) {
		t.Fatalf("detail collection = %v", detail["collection"])
	}
	if code, out := c.do("GET", "/api/collections/555", ""); code != 200 || out["collection"].(map[string]any)["name"] != "Saga" ||
		!equalIDs(ids(out["items"]), id["first"], id["second"]) {
		t.Fatalf("collection: %d %v", code, out)
	}
	if code, _ := c.do("GET", "/api/collections/1", ""); code != 404 {
		t.Fatalf("missing collection: %d", code)
	}
	if _, d := c.do("GET", "/api/items/"+itoa(id["show"]), ""); d["collection"] != nil {
		t.Fatalf("show has a collection: %v", d["collection"])
	}
}

func TestPersonEndpoint(t *testing.T) {
	_, c, id := browseServer(t)
	code, out := c.do("GET", "/api/person?name=ANN+ACTOR", "")
	if code != 200 || !equalIDs(ids(out["items"]), id["second"], id["first"]) {
		t.Fatalf("person: %d %v", code, out)
	}
	if out["name"] != "ann actor" && out["name"] != "Ann Actor" {
		t.Fatalf("name = %v", out["name"])
	}
	if roles := out["roles"].(map[string]any); roles[itoa(id["first"])] != "Hero" {
		t.Fatalf("roles = %v", roles)
	}
	if img := out["image"].(map[string]any); img["itemId"] != float64(id["first"]) || img["index"] != float64(0) {
		t.Fatalf("image = %v", img)
	}
	if _, out := c.do("GET", "/api/person?name=Nobody", ""); len(ids(out["items"])) != 0 || out["image"] != nil {
		t.Fatalf("nobody = %v", out)
	}
	if code, _ := c.do("GET", "/api/person", ""); code != 400 {
		t.Fatalf("no name: %d", code)
	}
}

func equalIDs(got []int64, want ...int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
