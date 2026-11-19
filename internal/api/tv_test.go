package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	c := s.St.Config()
	c.LiveTV = true
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
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
	// A window gives every program in it, back to back.
	from := time.Now().Add(-30 * time.Minute).UnixMilli()
	to := from + 12*3600_000
	var grid struct {
		Channels []channelView `json:"channels"`
		From     int64         `json:"from"`
		To       int64         `json:"to"`
	}
	if code := getJSON(t, h, token, "/api/tv?from="+strconv.FormatInt(from, 10)+"&to="+strconv.FormatInt(to, 10), &grid); code != 200 || len(grid.Channels) != 1 || grid.From != from || grid.To != to {
		t.Fatalf("grid: %d %+v", code, grid)
	}
	progs := grid.Channels[0].Programs
	if len(progs) <= guidePrograms || progs[0].Start > from || progs[len(progs)-1].End < to {
		t.Fatalf("grid programs: %d, %d..%d for %d..%d", len(progs), progs[0].Start, progs[len(progs)-1].End, from, to)
	}
	for i := 1; i < len(progs); i++ {
		if progs[i].Start != progs[i-1].End {
			t.Fatalf("gap at %d: %d after %d", i, progs[i].Start, progs[i-1].End)
		}
	}
	// A window too long is cut to a day.
	if getJSON(t, h, token, "/api/tv?from="+strconv.FormatInt(from, 10)+"&to="+strconv.FormatInt(from+72*3600_000, 10), &grid); grid.To-grid.From != int64(maxGuideWindow/time.Millisecond) {
		t.Fatalf("long window: %d..%d", grid.From, grid.To)
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

// Live TV is off until an admin turns it on: its API answers 404 and the
// UI's caps say so.
func TestLiveTVIsOffByDefault(t *testing.T) {
	s, h := securityServer(t)
	if s.St.Config().LiveTV {
		t.Fatal("Live TV is on by default")
	}
	u, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(u.ID, "Test", "127.0.0.1")
	for _, path := range []string{"/api/tv", "/api/tv/all"} {
		if code := getJSON(t, h, token, path, nil); code != 404 {
			t.Fatalf("%s: %d, want 404", path, code)
		}
	}
	var me struct {
		Caps struct {
			LiveTV bool `json:"liveTv"`
		} `json:"caps"`
	}
	if code := getJSON(t, h, token, "/api/me", &me); code != 200 || me.Caps.LiveTV {
		t.Fatalf("/api/me: %d, caps %v", code, me.Caps)
	}
	c := s.St.Config()
	c.LiveTV = true
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, h, token, "/api/tv", nil); code != 200 {
		t.Fatalf("turned on: %d", code)
	}
	me.Caps.LiveTV = false
	if code := getJSON(t, h, token, "/api/me", &me); code != 200 || !me.Caps.LiveTV {
		t.Fatalf("turned on: %d, caps %v", code, me.Caps)
	}
}

// An admin's channel leads the guide and tunes like any other.
// An admin can turn automatic channels off; the list of them (for Settings)
// is admin-only and includes the hidden ones.
func TestTVHiddenChannels(t *testing.T) {
	s, h := securityServer(t)
	token := tvLibrary(t, s)
	c := s.St.Config()
	c.TVChannels = []store.TVChannel{{ID: "eps", Name: "Episodes", Kind: "show"}}
	c.TVHidden = []string{"lib-1"}
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	var guide struct {
		Channels []channelView `json:"channels"`
	}
	if code := getJSON(t, h, token, "/api/tv", &guide); code != 200 || len(guide.Channels) != 1 || guide.Channels[0].ID != "custom-eps" {
		t.Fatalf("guide: %d %+v", code, guide)
	}
	if code := getJSON(t, h, token, "/api/tv/lib-1", nil); code != 404 {
		t.Fatalf("tuning a hidden channel: %d", code)
	}
	if code := getJSON(t, h, token, "/api/admin/tv", nil); code != 403 {
		t.Fatalf("lineup for a viewer: %d", code)
	}
	admin, err := s.St.CreateUser("boss", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	var l tvLineup
	if code := getJSON(t, h, adminToken, "/api/admin/tv", &l); code != 200 || len(l.Channels) != 2 {
		t.Fatalf("lineup: %d %+v", code, l)
	}
	eps, lib := l.Channels[0], l.Channels[1]
	if eps.ID != "custom-eps" || eps.Source != "custom" || eps.Custom == nil || eps.Custom.Name != "Episodes" || eps.Off || eps.Titles != 1 {
		t.Fatalf("custom channel = %+v", eps)
	}
	if lib.ID != "lib-1" || lib.Name != "Everything" || lib.Source != "library" || !lib.Off || lib.Number != 2 || lib.Titles != 2 || lib.Custom != nil {
		t.Fatalf("library channel = %+v", lib)
	}
}

// Settings › Live TV saves each change on its own: the switch, the order,
// the channels turned off, and each of the admin's channels.
func TestTVLineupEditing(t *testing.T) {
	s, h := securityServer(t)
	viewer := tvLibrary(t, s)
	admin, err := s.St.CreateUser("boss", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	send := func(method, path, body string, want int) tvLineup {
		t.Helper()
		w := adminRequest(t, h, token, method, path, body)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
		}
		var l tvLineup
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
				t.Fatal(err)
			}
		}
		return l
	}
	ids := func(l tvLineup) (out []string) {
		for _, c := range l.Channels {
			out = append(out, c.ID)
		}
		return out
	}
	if w := adminRequest(t, h, viewer, "POST", "/api/admin/tv/channels", `{"name":"Mine"}`); w.Code != 403 {
		t.Fatalf("viewer adds a channel: %d", w.Code)
	}
	send("POST", "/api/admin/tv/channels", `{"name":"  "}`, 400)
	send("POST", "/api/admin/tv/channels", `{"name":"Picks","kind":"picked","titles":[]}`, 400)
	// A new channel goes after the automatic ones.
	l := send("POST", "/api/admin/tv/channels", `{"name":" Movies ","kind":"movie","genres":["","Crime","Crime"]}`, 200)
	id := l.Saved
	if !strings.HasPrefix(id, "custom-") || !slices.Equal(ids(l), []string{"lib-1", id}) {
		t.Fatalf("after adding: %v, saved %q", ids(l), id)
	}
	if c := l.Channels[1].Custom; c.Name != "Movies" || !slices.Equal(c.Genres, []string{"Crime"}) || l.Channels[1].Number != 2 {
		t.Fatalf("added = %+v", l.Channels[1])
	}
	custom := strings.TrimPrefix(id, "custom-")
	l = send("PUT", "/api/admin/tv/channels/"+custom, `{"name":"Heat only","kind":"picked","titles":[1]}`, 200)
	if c := l.Channels[1]; c.Name != "Heat only" || c.Titles != 1 || c.Custom.ID != custom {
		t.Fatalf("edited = %+v", c)
	}
	send("PUT", "/api/admin/tv/channels/nope", `{"name":"X"}`, 404)
	// Reorder and turn one off; the switch saves alone.
	l = send("PUT", "/api/admin/tv", `{"order":["`+id+`","lib-1"],"off":["lib-1"]}`, 200)
	if !slices.Equal(ids(l), []string{id, "lib-1"}) || !l.Channels[1].Off || l.Enabled != true {
		t.Fatalf("after reordering: %+v", l)
	}
	if l = send("PUT", "/api/admin/tv", `{"enabled":false}`, 200); l.Enabled || !l.Channels[1].Off || l.Channels[0].ID != id {
		t.Fatalf("after turning Live TV off: %+v", l)
	}
	send("PUT", "/api/admin/tv", `{"enabled":true}`, 200)
	// The settings form doesn't touch any of it, even sending an old copy.
	if w := adminRequest(t, h, token, "PUT", "/api/admin/config", `{"liveTv":false,"tvChannels":[],"tvOrder":[],"tvHidden":[],"serverName":"Den"}`); w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body.String())
	}
	if c := s.St.Config(); !c.LiveTV || len(c.TVChannels) != 1 || len(c.TVHidden) != 1 || c.ServerName != "Den" {
		t.Fatalf("config PUT changed Live TV: %+v", c)
	}
	// The preview counts titles and shows what's on.
	var prev struct {
		Titles   int           `json:"titles"`
		Programs []programView `json:"programs"`
	}
	w := adminRequest(t, h, token, "POST", "/api/admin/tv/preview", `{"kind":"show"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &prev); err != nil || w.Code != 200 || prev.Titles != 1 || len(prev.Programs) != previewPrograms || prev.Programs[0].Item.Kind != "episode" {
		t.Fatalf("preview: %d %+v", w.Code, prev)
	}
	// Removing a channel drops it from the order too.
	l = send("DELETE", "/api/admin/tv/channels/"+custom, "", 200)
	if !slices.Equal(ids(l), []string{"lib-1"}) || len(s.St.Config().TVOrder) != 1 {
		t.Fatalf("after removing: %v, order %v", ids(l), s.St.Config().TVOrder)
	}
	send("DELETE", "/api/admin/tv/channels/"+custom, "", 404)
}

func TestTVCustomChannel(t *testing.T) {
	s, h := securityServer(t)
	token := tvLibrary(t, s)
	c := s.St.Config()
	c.TVChannels = []store.TVChannel{{ID: "eps", Name: "Episodes", Kind: "show"}}
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	var guide struct {
		Channels []channelView `json:"channels"`
	}
	if code := getJSON(t, h, token, "/api/tv", &guide); code != 200 || len(guide.Channels) != 2 {
		t.Fatalf("guide: %d %+v", code, guide)
	}
	if ch := guide.Channels[0]; ch.ID != "custom-eps" || ch.Name != "Episodes" || ch.Number != 1 {
		t.Fatalf("first channel = %+v", ch)
	}
	var tuned struct {
		Now  programView  `json:"now"`
		Next *programView `json:"next"`
	}
	if code := getJSON(t, h, token, "/api/tv/custom-eps", &tuned); code != 200 {
		t.Fatalf("tune: %d", code)
	}
	for _, p := range []*programView{&tuned.Now, tuned.Next} {
		if p == nil || p.Item.Kind != "episode" {
			t.Fatalf("Episodes aired %+v", p)
		}
	}
}
