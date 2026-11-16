package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lex/internal/cache"
	"lex/internal/meta"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/trickplay"
)

// accessFixture is a server with two libraries, Family (1) and Private (2),
// each with a movie and a show, and three users: an admin, a viewer who
// sees every library, and a kid restricted to Family.
type accessFixture struct {
	s                *Server
	h                http.Handler
	admin, all, kid  string // tokens
	kidID            int64
	familyMovieFile  int64
	privateMovieFile int64
	privateEpFile    int64
	privateSub       int // downloaded subtitle index on the private movie
	familySub        int
}

// Ids: Family has movie 1, show 2 (season 3, episodes 4 and 5); Private has
// movie 11, show 12 (season 13, episodes 14 and 15). Item 99 doesn't exist.
const (
	familyMovie, familyShow, familySeason, familyEp     = 1, 2, 3, 4
	privateMovie, privateShow, privateSeason, privateEp = 11, 12, 13, 14
	missingItem                                         = 99
	missingFile                                         = 999
)

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	s, _ := securityServer(t)
	dir := t.TempDir()
	s.DataDir = dir
	s.Images = &meta.ImageCache{Dir: filepath.Join(dir, "images")}
	s.Cache = cache.New(s.St, s.Log, dir)
	s.Sess = stream.NewManager(s.St, s.Log)
	s.Subs = stream.NewSubs(filepath.Join(dir, "subs"), "", "", s.Log)
	s.HLS = stream.NewHLS(filepath.Join(dir, "hls"), "", s.Log)
	s.Trick = trickplay.New(s.St, s.Log, "", filepath.Join(dir, "trick"))
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{s: s, h: h}

	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	db := s.St.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Family','mixed','[]',0),(2,'Private','mixed','[]',0)`)
	for _, lib := range []struct {
		id    int64
		base  int64
		title string
		genre string
	}{{1, 0, "Family", "Animation"}, {2, 10, "Secret", "Noir"}} {
		b := lib.base
		genres := `["` + lib.genre + `"]`
		cast := `[{"name":"Actor"}]` // no image: nothing is fetched
		exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,genres,cast_json,added_at,updated_at) VALUES(?,?,'movie',0,?,?,?,?,1,1)`,
			b+1, lib.id, lib.title+" Movie", strings.ToLower(lib.title)+" movie", genres, cast)
		exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,genres,added_at,updated_at) VALUES(?,?,'show',0,?,?,?,1,1)`,
			b+2, lib.id, lib.title+" Show", strings.ToLower(lib.title)+" show", genres)
		exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,added_at,updated_at) VALUES(?,?,'season',?,?,'Season 1','season 1',1,1,1)`,
			b+3, lib.id, b+2, b+2)
		for ep := int64(1); ep <= 2; ep++ {
			exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,episode,added_at,updated_at) VALUES(?,?,'episode',?,?,?,?,1,?,1,1)`,
				b+3+ep, lib.id, b+3, b+2, lib.title+" Episode", "ep", ep)
		}
		for _, item := range []int64{b + 1, b + 4, b + 5} {
			p := filepath.Join(media, strconv.FormatInt(item, 10)+".mp4")
			if err := os.WriteFile(p, []byte("not really a video"), 0o644); err != nil {
				t.Fatal(err)
			}
			fid, err := s.St.InsertFile(&store.File{ItemID: item, LibraryID: lib.id, Path: p, Size: 18, AddedAt: 1})
			if err != nil {
				t.Fatal(err)
			}
			info := &store.MediaInfo{Format: "mov,mp4,m4a,3gp,3g2,mj2", Duration: 1800, Bitrate: 1_000_000, Streams: []store.Stream{
				{Index: 0, Type: "video", Codec: "h264", Width: 1280, Height: 720},
				{Index: 1, Type: "audio", Codec: "aac", Channels: 2},
			}}
			if err := s.St.SaveProbe(fid, info, ""); err != nil {
				t.Fatal(err)
			}
			switch item {
			case familyMovie:
				f.familyMovieFile = fid
			case privateMovie:
				f.privateMovieFile = fid
			case privateEp:
				f.privateEpFile = fid
			}
		}
	}

	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.St.CreateUser("everyone", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	kid, err := s.St.CreateUser("kid", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	f.kidID = kid.ID
	f.admin, _ = s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	f.all, _ = s.St.CreateToken(all.ID, "Test", "127.0.0.1")
	f.kid, _ = s.St.CreateToken(kid.ID, "Test", "127.0.0.1")

	// What the kid did before being restricted: half-watched the private
	// movie, finished a private episode, favorited the private show, and
	// downloaded subtitles for both movies.
	exec(`INSERT INTO user_data(user_id,item_id,position,played,last_played,favorite) VALUES(?,?,600,0,100,0),(?,?,0,1,200,0),(?,?,0,0,0,1)`,
		kid.ID, privateMovie, kid.ID, privateEp, kid.ID, privateShow)
	for _, fid := range []int64{f.privateMovieFile, f.familyMovieFile} {
		p := filepath.Join(dir, strconv.FormatInt(fid, 10)+".srt")
		os.WriteFile(p, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644)
		d := &store.DownloadedSub{FileID: fid, Language: "eng", Path: p, UserID: kid.ID}
		if _, err := s.St.AddDownloadedSub(d); err != nil {
			t.Fatal(err)
		}
		if fid == f.privateMovieFile {
			f.privateSub = store.DownloadedSubBase + int(d.ID)
		} else {
			f.familySub = store.DownloadedSubBase + int(d.ID)
		}
	}

	c := s.St.Config()
	c.LiveTV = true
	if _, err := s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}

	// The admin restricts the kid to Family, through the API.
	if code, body := f.do(t, f.admin, "PUT", "/api/admin/users/"+strconv.FormatInt(kid.ID, 10), `{"allLibraries":false,"libraries":[1]}`); code != 200 {
		t.Fatalf("restrict kid: %d %s", code, body)
	}
	return f
}

func (f *accessFixture) do(t *testing.T, token, method, path, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, "http://lex.test"+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func (f *accessFixture) getJSON(t *testing.T, token, path string, out any) {
	t.Helper()
	code, body := f.do(t, token, "GET", path, "")
	if code != 200 {
		t.Fatalf("GET %s: %d %s", path, code, body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

func itemIDs(items []*store.Item) []int64 {
	out := []int64{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func noPrivate(t *testing.T, what string, ids []int64) {
	t.Helper()
	for _, id := range ids {
		if id > 10 {
			t.Errorf("%s shows private item %d: %v", what, id, ids)
		}
	}
}

// Every endpoint that takes an item or file id answers a restricted user
// asking for something in a library they can't see exactly like an id that
// doesn't exist (so ids don't leak), while the same request for their own
// library, or by a user who may see it, gets through.
func TestRestrictedUserCannotReachOtherLibrariesByID(t *testing.T) {
	f := newAccessFixture(t)
	item := func(id int64) string { return strconv.FormatInt(id, 10) }
	type req struct{ method, path, body string }
	type endpoint struct {
		name string
		// make builds the request for an item (or file) id.
		make func(id int64) req
		// private, missing and allowed are the ids to try.
		private, allowed, missing int64
		files                     bool
	}
	itemEP := func(name, method, suffix, body string) endpoint {
		return endpoint{name: name, make: func(id int64) req { return req{method, "/api/items/" + item(id) + suffix, body} },
			private: privateMovie, allowed: familyMovie, missing: missingItem}
	}
	// with uses other ids for an item endpoint.
	with := func(e endpoint, private, allowed int64) endpoint {
		e.private, e.allowed = private, allowed
		return e
	}
	fileEP := func(name, method, suffix, body string) endpoint {
		return endpoint{name: name, make: func(id int64) req { return req{method, "/api/files/" + item(id) + suffix, body} },
			private: f.privateMovieFile, allowed: f.familyMovieFile, missing: missingFile, files: true}
	}
	endpoints := []endpoint{
		itemEP("detail", "GET", "", ""),
		with(itemEP("detail of an episode", "GET", "", ""), privateEp, familyEp),
		with(itemEP("detail of a show", "GET", "", ""), privateShow, familyShow),
		with(itemEP("children of a show", "GET", "/children", ""), privateShow, familyShow),
		with(itemEP("children of a season", "GET", "/children", ""), privateSeason, familySeason),
		with(itemEP("shuffle a show", "GET", "/shuffle", ""), privateShow, familyShow),
		with(itemEP("shuffle a season", "GET", "/shuffle", ""), privateSeason, familySeason),
		itemEP("poster", "GET", "/image/poster", ""),
		itemEP("backdrop", "GET", "/image/backdrop", ""),
		itemEP("cast image", "GET", "/cast/0/image", ""),
		itemEP("mark played", "POST", "/played", `{"played":true}`),
		with(itemEP("mark a show played", "POST", "/played", `{"played":true}`), privateShow, familyShow),
		itemEP("favorite", "POST", "/favorite", `{"favorite":true}`),
		itemEP("watchlist", "POST", "/watchlist", `{"watchlist":true}`),
		with(itemEP("watchlist a show", "POST", "/watchlist", `{"watchlist":true}`), privateShow, familyShow),
		{name: "playback plan", make: func(id int64) req {
			return req{"POST", "/api/playback/plan", `{"itemId":` + item(id) + `,"canDirect":true,"containers":["mp4"],"videoCodecs":["h264"],"audioCodecs":["aac"]}`}
		}, private: privateMovie, allowed: familyMovie, missing: missingItem},
		fileEP("direct", "GET", "/direct", ""),
		fileEP("direct HEAD", "HEAD", "/direct", ""),
		fileEP("stream", "GET", "/stream?video=copy&audio=copy", ""),
		fileEP("HLS playlist", "GET", "/hls/index.m3u8?sid=x&video=copy", ""),
		fileEP("HLS init", "GET", "/hls/init.mp4?sid=x&video=copy", ""),
		fileEP("HLS segment", "GET", "/hls/seg/0.m4s?sid=x&video=copy", ""),
		fileEP("embedded subtitle", "GET", "/subs/2.vtt", ""),
		fileEP("subtitle as ASS", "GET", "/subs/2.ass", ""),
		fileEP("subtitle search", "GET", "/subsearch?lang=eng", ""),
		fileEP("subtitle download", "POST", "/subsearch", `{}`),
		fileEP("fonts", "GET", "/fonts", ""),
		fileEP("font", "GET", "/fonts/a.ttf", ""),
		fileEP("trickplay", "GET", "/trickplay", ""),
		fileEP("trickplay sheet", "GET", "/trickplay/1.jpg", ""),
	}
	// A downloaded subtitle is served by its index on its file.
	endpoints = append(endpoints, endpoint{name: "downloaded subtitle", make: func(id int64) req {
		idx := f.familySub
		if id == f.privateMovieFile {
			idx = f.privateSub
		}
		return req{"GET", "/api/files/" + item(id) + "/subs/" + strconv.Itoa(idx) + ".vtt", ""}
	}, private: f.privateMovieFile, allowed: f.familyMovieFile, missing: missingFile, files: true})

	for _, e := range endpoints {
		t.Run(e.name, func(t *testing.T) {
			send := func(token string, id int64) (int, string) {
				r := e.make(id)
				return f.do(t, token, r.method, r.path, r.body)
			}
			missCode, missBody := send(f.kid, e.missing)
			if missCode != 404 {
				t.Fatalf("missing id: %d %s", missCode, missBody)
			}
			code, body := send(f.kid, e.private)
			if code != missCode || body != missBody {
				t.Fatalf("private id: %d %q, want what a missing id gets: %d %q", code, body, missCode, missBody)
			}
			// The check isn't just everything answering 404.
			if c, b := send(f.kid, e.allowed); c == missCode && b == missBody {
				t.Fatalf("kid, own library: %d %q", c, b)
			}
			for who, tok := range map[string]string{"admin": f.admin, "unrestricted user": f.all} {
				if c, b := send(tok, e.private); c == missCode && b == missBody {
					t.Fatalf("%s on the private id: %d %q", who, c, b)
				}
			}
		})
	}
	// None of that opened a play session on a private file or changed the
	// kid's data on private items.
	for _, snap := range f.s.Sess.Snapshot() {
		if snap.UserName == "kid" && (snap.FileID == f.privateMovieFile || snap.FileID == f.privateEpFile || snap.ItemID > 10) {
			t.Errorf("kid has a session on private file %d", snap.FileID)
		}
	}
	var played, fav int
	f.s.St.DB().QueryRow(`SELECT COALESCE(SUM(played),0), COALESCE(SUM(favorite),0) FROM user_data WHERE user_id=? AND item_id IN (?,?,?)`,
		f.kidID, privateMovie, privateEp, privateEp+1).Scan(&played, &fav)
	if played != 1 || fav != 0 { // only the episode finished before the restriction
		t.Errorf("kid's private user data changed: played=%d favorite=%d", played, fav)
	}
}

// Deleting a downloaded subtitle on a file the user can't see is a 404 even
// for the user who downloaded it, and the subtitle stays.
func TestRestrictedUserCannotDeleteSubtitlesElsewhere(t *testing.T) {
	f := newAccessFixture(t)
	del := func(file int64, idx int) (int, string) {
		return f.do(t, f.kid, "DELETE", "/api/files/"+strconv.FormatInt(file, 10)+"/subs/"+strconv.Itoa(idx), "")
	}
	missCode, missBody := del(missingFile, f.privateSub)
	if code, body := del(f.privateMovieFile, f.privateSub); code != 404 || code != missCode || body != missBody {
		t.Fatalf("private: %d %s", code, body)
	}
	if _, err := f.s.St.DownloadedSub(int64(f.privateSub - store.DownloadedSubBase)); err != nil {
		t.Fatalf("private subtitle was deleted: %v", err)
	}
	if code, body := del(f.familyMovieFile, f.familySub); code != 200 {
		t.Fatalf("own library: %d %s", code, body)
	}
}

// Progress for a file the user can't see doesn't re-open a play session.
func TestRestrictedUserProgressCannotOpenSessions(t *testing.T) {
	f := newAccessFixture(t)
	body := `{"sessionId":"s1","fileId":` + strconv.FormatInt(f.privateMovieFile, 10) + `,"position":30}`
	if code, b := f.do(t, f.kid, "POST", "/api/playback/progress", body); code != 404 {
		t.Fatalf("progress: %d %s", code, b)
	}
	if f.s.Sess.Get("s1") != nil {
		t.Fatal("progress opened a session on a private file")
	}
	body = `{"sessionId":"s2","fileId":` + strconv.FormatInt(f.familyMovieFile, 10) + `,"position":30}`
	if code, b := f.do(t, f.kid, "POST", "/api/playback/progress", body); code != 200 {
		t.Fatalf("own library progress: %d %s", code, b)
	}
}

// Lists only ever show what the user may see.
func TestRestrictedUserListsHideOtherLibraries(t *testing.T) {
	f := newAccessFixture(t)

	var libs []store.Library
	f.getJSON(t, f.kid, "/api/libraries", &libs)
	if len(libs) != 1 || libs[0].ID != 1 {
		t.Fatalf("libraries: %+v", libs)
	}
	f.getJSON(t, f.all, "/api/libraries", &libs)
	if len(libs) != 2 {
		t.Fatalf("unrestricted libraries: %+v", libs)
	}

	var home struct {
		Rows []struct {
			ID    string        `json:"id"`
			Items []*store.Item `json:"items"`
		} `json:"rows"`
		Libraries []store.Library `json:"libraries"`
	}
	f.getJSON(t, f.kid, "/api/home", &home)
	if len(home.Libraries) != 1 || home.Libraries[0].ID != 1 {
		t.Fatalf("home libraries: %+v", home.Libraries)
	}
	for _, row := range home.Rows {
		if row.ID == "continue" || row.ID == "nextup" || row.ID == "lib-2" {
			t.Errorf("home has row %s (%v)", row.ID, itemIDs(row.Items))
		}
		noPrivate(t, "home row "+row.ID, itemIDs(row.Items))
	}

	var list struct {
		Items []*store.Item `json:"items"`
		Total int           `json:"total"`
	}
	for _, q := range []string{"", "?library=2", "?kind=movie", "?kind=episode", "?filter=inprogress", "?filter=favorite", "?filter=played",
		"?sort=played&desc=1", "?sort=random&seed=7", "?q=secret", "?genre=Noir", "?filter=watchlist"} {
		f.getJSON(t, f.kid, "/api/items"+q, &list)
		noPrivate(t, "items"+q, itemIDs(list.Items))
		if want := len(list.Items); list.Total != want {
			t.Errorf("items%s: total %d counts hidden items (%d shown)", q, list.Total, want)
		}
	}
	f.getJSON(t, f.kid, "/api/items?library=2", &list)
	if len(list.Items) != 0 || list.Total != 0 {
		t.Fatalf("private library listed: %+v", list)
	}
	f.getJSON(t, f.all, "/api/items?library=2", &list)
	if list.Total != 2 {
		t.Fatalf("unrestricted user, private library: total %d", list.Total)
	}

	var genres []string
	for _, q := range []string{"", "?library=2"} {
		f.getJSON(t, f.kid, "/api/genres"+q, &genres)
		if slices.Contains(genres, "Noir") {
			t.Errorf("genres%s: %v", q, genres)
		}
	}
	f.getJSON(t, f.admin, "/api/genres", &genres)
	if !slices.Contains(genres, "Noir") {
		t.Fatalf("admin genres: %v", genres)
	}

	// Letter counts cover only what /api/items lists.
	for _, q := range []string{"", "?library=2", "?kind=movie", "?genre=Noir"} {
		var counts map[string]int
		f.getJSON(t, f.kid, "/api/letters"+q, &counts)
		f.getJSON(t, f.kid, "/api/items"+q, &list)
		n := 0
		for _, c := range counts {
			n += c
		}
		if n != list.Total {
			t.Errorf("letters%s: %v counts %d titles, items lists %d", q, counts, n, list.Total)
		}
	}
	var adminCounts map[string]int
	f.getJSON(t, f.admin, "/api/letters?library=2", &adminCounts)
	if len(adminCounts) == 0 {
		t.Fatalf("admin letters for the private library: %v", adminCounts)
	}

	var search map[string][]*store.Item
	f.getJSON(t, f.kid, "/api/search?q=e", &search)
	for k, v := range search {
		noPrivate(t, "search "+k, itemIDs(v))
	}
	f.getJSON(t, f.kid, "/api/search?q=secret", &search)
	if n := len(search["movies"]) + len(search["shows"]) + len(search["episodes"]); n != 0 {
		t.Fatalf("search for a private title: %v", search)
	}
	f.getJSON(t, f.admin, "/api/search?q=secret", &search)
	if len(search["movies"]) != 1 || len(search["shows"]) != 1 || len(search["episodes"]) != 2 {
		t.Fatalf("admin search: %v", search)
	}

	// The kid's own activity in Family still shows up.
	if code, b := f.do(t, f.kid, "POST", "/api/items/4/played", `{"played":true}`); code != 200 {
		t.Fatalf("played: %d %s", code, b)
	}
	f.getJSON(t, f.kid, "/api/home", &home)
	var nextUp []int64
	for _, row := range home.Rows {
		if row.ID == "nextup" {
			nextUp = itemIDs(row.Items)
		}
	}
	if !slices.Equal(nextUp, []int64{5}) {
		t.Fatalf("next up: %v, want the family episode 5", nextUp)
	}
}

// Live TV only lists, and tunes into, channels that air nothing from a
// library the user can't see: every viewer gets the same schedule, so a
// channel mixing libraries is hidden rather than edited.
func TestRestrictedUserLiveTV(t *testing.T) {
	f := newAccessFixture(t)
	c := f.s.St.Config()
	c.TVChannels = []store.TVChannel{
		{ID: "fam", Name: "Family picks", Kind: "picked", Titles: []int64{familyMovie, familyShow}},
		{ID: "mix", Name: "Mixed picks", Kind: "picked", Titles: []int64{familyMovie, privateMovie}},
	}
	if _, err := f.s.St.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	guide := func(token string) []string {
		var g struct {
			Channels []channelView `json:"channels"`
		}
		f.getJSON(t, token, "/api/tv", &g)
		var ids []string
		for _, ch := range g.Channels {
			ids = append(ids, ch.ID)
			for _, p := range ch.Programs {
				if token == f.kid && p.Item.LibraryID != 1 {
					t.Errorf("kid's guide airs private item %d on %s", p.Item.ID, ch.ID)
				}
			}
		}
		return ids
	}
	kid := guide(f.kid)
	for _, want := range []string{"custom-fam", "lib-1"} {
		if !slices.Contains(kid, want) {
			t.Errorf("kid's guide lacks %s: %v", want, kid)
		}
	}
	for _, hidden := range []string{"custom-mix", "all", "lib-2"} {
		if slices.Contains(kid, hidden) {
			t.Errorf("kid's guide has %s: %v", hidden, kid)
		}
	}
	if all := guide(f.all); !slices.Contains(all, "lib-2") || !slices.Contains(all, "custom-mix") || !slices.Contains(all, "all") {
		t.Fatalf("unrestricted guide: %v", all)
	}
	missCode, missBody := f.do(t, f.kid, "GET", "/api/tv/nope", "")
	for _, ch := range []string{"custom-mix", "all", "lib-2"} {
		if code, body := f.do(t, f.kid, "GET", "/api/tv/"+ch, ""); code != missCode || body != missBody {
			t.Errorf("tune %s: %d %s", ch, code, body)
		}
		if code, body := f.do(t, f.admin, "GET", "/api/tv/"+ch, ""); code != 200 {
			t.Errorf("admin tune %s: %d %s", ch, code, body)
		}
	}
	for _, ch := range []string{"custom-fam", "lib-1"} {
		if code, body := f.do(t, f.kid, "GET", "/api/tv/"+ch, ""); code != 200 {
			t.Errorf("kid tune %s: %d %s", ch, code, body)
		}
	}
}

// Admins manage access in Settings › Users; the default is every library,
// so existing users are unchanged, and removing a library drops it.
func TestLibraryAccessAdministration(t *testing.T) {
	f := newAccessFixture(t)
	var users []store.User
	f.getJSON(t, f.admin, "/api/admin/users", &users)
	byName := map[string]store.User{}
	for _, u := range users {
		byName[u.Name] = u
	}
	if u := byName["everyone"]; !u.AllLibraries || len(u.Libraries) != 0 {
		t.Fatalf("default access: %+v", u)
	}
	if u := byName["kid"]; u.AllLibraries || !slices.Equal(u.Libraries, []int64{1}) {
		t.Fatalf("kid's access: %+v", u)
	}
	// Regular users can't change access, their own or anyone's.
	kidPath := "/api/admin/users/" + strconv.FormatInt(f.kidID, 10)
	if code, _ := f.do(t, f.kid, "PUT", kidPath, `{"allLibraries":true}`); code != 403 {
		t.Fatalf("kid changed own access: %d", code)
	}
	// Unknown libraries are ignored; nothing at all is allowed.
	if code, b := f.do(t, f.admin, "PUT", kidPath, `{"allLibraries":false,"libraries":[2,404]}`); code != 200 {
		t.Fatalf("set access: %d %s", code, b)
	}
	u, _ := f.s.St.User(f.kidID)
	if u.AllLibraries || !slices.Equal(u.Libraries, []int64{2}) {
		t.Fatalf("after update: %+v", u)
	}
	if err := f.s.St.DeleteLibrary(2); err != nil {
		t.Fatal(err)
	}
	u, _ = f.s.St.User(f.kidID)
	if u.AllLibraries || len(u.Libraries) != 0 {
		t.Fatalf("after deleting the library: %+v", u)
	}
	var libs []store.Library
	f.getJSON(t, f.kid, "/api/libraries", &libs)
	if len(libs) != 0 {
		t.Fatalf("kid with no libraries sees %+v", libs)
	}
	// A password reset alone leaves access as it was.
	if code, b := f.do(t, f.admin, "PUT", kidPath, `{"password":"another-password-1"}`); code != 200 {
		t.Fatalf("reset: %d %s", code, b)
	}
	if u, _ = f.s.St.User(f.kidID); u.AllLibraries {
		t.Fatal("password reset changed access")
	}
	f.kid, _ = f.s.St.CreateToken(f.kidID, "Test", "127.0.0.1") // the reset signed them out
	// New users can be created restricted.
	code, b := f.do(t, f.admin, "POST", "/api/admin/users", `{"name":"guest","password":"test-password-123","allLibraries":false,"libraries":[1]}`)
	var created store.User
	json.Unmarshal([]byte(b), &created)
	if code != 200 || created.AllLibraries || !slices.Equal(created.Libraries, []int64{1}) {
		t.Fatalf("create restricted: %d %s", code, b)
	}
	// Admins see everything regardless.
	if code, b := f.do(t, f.admin, "PUT", kidPath, `{"isAdmin":true}`); code != 200 {
		t.Fatalf("make admin: %d %s", code, b)
	}
	f.getJSON(t, f.kid, "/api/libraries", &libs)
	if len(libs) != 1 || libs[0].ID != 1 {
		t.Fatalf("kid as admin sees %+v", libs)
	}
}

// The watchlist, collections and people pages only show what the user may
// see, and a collection with none of its movies in the user's libraries
// doesn't exist for them.
func TestRestrictedUserBrowsePagesHideOtherLibraries(t *testing.T) {
	f := newAccessFixture(t)
	st := f.s.St
	// Both movies share a collection; the private one has its own too. The
	// kid put the private movie on the watchlist before being restricted.
	if err := st.SetItemCollection(familyMovie, &store.Collection{ID: 500, Name: "Saga"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetItemCollection(privateMovie, &store.Collection{ID: 500, Name: "Saga"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO collections(id,name) VALUES(501,'Secret Saga')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO item_collections(item_id,collection_id) VALUES(?,501) ON CONFLICT(item_id) DO UPDATE SET collection_id=501`, privateShow); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO watchlist(user_id,item_id,added_at) VALUES(?,?,1),(?,?,2)`, f.kidID, privateMovie, f.kidID, familyMovie); err != nil {
		t.Fatal(err)
	}

	var wl struct {
		Items []*store.Item `json:"items"`
	}
	f.getJSON(t, f.kid, "/api/watchlist", &wl)
	if ids := itemIDs(wl.Items); !slices.Equal(ids, []int64{familyMovie}) {
		t.Errorf("watchlist: %v", ids)
	}
	var coll struct {
		Items []*store.Item `json:"items"`
	}
	f.getJSON(t, f.kid, "/api/collections/500", &coll)
	if ids := itemIDs(coll.Items); !slices.Equal(ids, []int64{familyMovie}) {
		t.Errorf("collection: %v", ids)
	}
	f.getJSON(t, f.all, "/api/collections/500", &coll)
	if len(coll.Items) != 2 {
		t.Errorf("unrestricted collection: %v", itemIDs(coll.Items))
	}
	missCode, missBody := f.do(t, f.kid, "GET", "/api/collections/999", "")
	if code, body := f.do(t, f.kid, "GET", "/api/collections/501", ""); code != 404 || code != missCode || body != missBody {
		t.Errorf("private collection: %d %s", code, body)
	}
	var person struct {
		Items []*store.Item `json:"items"`
	}
	f.getJSON(t, f.kid, "/api/person?name=Actor", &person)
	if ids := itemIDs(person.Items); !slices.Equal(ids, []int64{familyMovie}) {
		t.Errorf("person: %v", ids)
	}
	f.getJSON(t, f.all, "/api/person?name=Actor", &person)
	if len(person.Items) != 2 {
		t.Errorf("unrestricted person: %v", itemIDs(person.Items))
	}
}
