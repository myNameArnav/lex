package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"lex/internal/store"
)

// tvLibrary adds a show (a special and three episodes) and a movie, with
// durations, and returns a viewer's token.
func tvLibrary(t *testing.T, s *Server) string {
	t.Helper()
	db := s.St.DB()
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Everything','mixed','[]',0)`,
		`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES
			(1,1,'movie',0,'Heat','heat',0,0),(2,1,'show',0,'Show','show',0,0)`,
		`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,episode,added_at,updated_at) VALUES
			(3,1,'season',2,2,'Specials','specials',0,0,0,0),(4,1,'season',2,2,'Season 1','season 1',1,0,0,0),
			(5,1,'episode',3,2,'Special','special',0,1,0,0),
			(6,1,'episode',4,2,'One','one',1,1,0,0),(7,1,'episode',4,2,'Two','two',1,2,0,0),(8,1,'episode',4,2,'Three','three',1,3,0,0)`,
		`INSERT INTO files(item_id,library_id,path,size,mtime,added_at,duration) VALUES
			(1,1,'/m/heat.mkv',1,0,0,10200),(5,1,'/tv/s0e1.mkv',1,0,0,1300),(6,1,'/tv/1.mkv',1,0,0,1300),(7,1,'/tv/2.mkv',1,0,0,1300),(8,1,'/tv/3.mkv',1,0,0,1300)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(u.ID, "Test", "127.0.0.1")
	return token
}

func getJSON(t *testing.T, h http.Handler, token, path string, out any) int {
	t.Helper()
	r := httptest.NewRequest("GET", "http://lex.test"+path, nil)
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 && out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code
}

func TestShuffleEpisodes(t *testing.T) {
	s, h := securityServer(t)
	token := tvLibrary(t, s)
	var eps []*store.Item
	if code := getJSON(t, h, token, "/api/items/2/shuffle", &eps); code != 200 {
		t.Fatalf("show: %d", code)
	}
	seen := map[int64]bool{}
	for _, e := range eps {
		seen[e.ID] = true
	}
	if len(eps) != 3 || !seen[6] || !seen[7] || !seen[8] {
		t.Fatalf("show shuffle = %v, want the three regular episodes", seen)
	}
	if code := getJSON(t, h, token, "/api/items/3/shuffle", &eps); code != 200 || len(eps) != 1 || eps[0].ID != 5 {
		t.Fatalf("specials season: %d %d episodes", code, len(eps))
	}
	if code := getJSON(t, h, token, "/api/items/1/shuffle", nil); code != 400 {
		t.Fatalf("movie: %d, want 400", code)
	}
}

func TestTVGuideAndTuning(t *testing.T) {
	s, h := securityServer(t)
	token := tvLibrary(t, s)
	var guide struct {
		Channels []channelView `json:"channels"`
	}
	if code := getJSON(t, h, token, "/api/tv", &guide); code != 200 || len(guide.Channels) != 1 {
		t.Fatalf("guide: %d %+v", code, guide)
	}
	ch := guide.Channels[0]
	if ch.ID != "lib-1" || ch.Number != 1 || len(ch.Programs) == 0 || ch.Programs[0].Item == nil {
		t.Fatalf("channel = %+v", ch)
	}
	var tuned struct {
		Now        programView  `json:"now"`
		Next       *programView `json:"next"`
		ServerTime int64        `json:"serverTime"`
	}
	if code := getJSON(t, h, token, "/api/tv/lib-1", &tuned); code != 200 {
		t.Fatalf("tune: %d", code)
	}
	if tuned.Now.Start > tuned.ServerTime || tuned.Now.End <= tuned.ServerTime || tuned.Next == nil || tuned.Next.Start != tuned.Now.End {
		t.Fatalf("tuned = %+v", tuned)
	}
	if tuned.Now.Item.ID == 5 {
		t.Fatal("aired a special")
	}
	// Asking for the end of the current program tunes into the next one.
	var after struct {
		Now programView `json:"now"`
	}
	getJSON(t, h, token, "/api/tv/lib-1?at="+strconv.FormatInt(tuned.Now.End, 10), &after)
	if tuned.Now.End-tuned.ServerTime < 3600_000 && after.Now.Start != tuned.Now.End {
		t.Fatalf("at the end: now starts %d, want %d", after.Now.Start, tuned.Now.End)
	}
	if code := getJSON(t, h, token, "/api/tv/nope", nil); code != 404 {
		t.Fatalf("unknown channel: %d", code)
	}
	// A title that left the library makes the schedule start over without it.
	gone := tuned.Now.Item.ID
	if _, err := s.St.DB().Exec(`DELETE FROM items WHERE id=?`, gone); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, token, "/api/tv/lib-1", &tuned); code != 200 || tuned.Now.Item.ID == gone {
		t.Fatalf("after removing %d: %d, now %d", gone, code, tuned.Now.Item.ID)
	}
}
