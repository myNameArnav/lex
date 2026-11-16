package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"lex/internal/cache"
	"lex/internal/logx"
	"lex/internal/store"
)

// benchLibrary fills a synthetic library of about 5,400 items: 2,000 movies
// and 60 shows of 5 seasons x 10 episodes (plus specials), with the metadata
// a matched library carries (overview, cast, studios, provider ids), and a
// viewer who has watched some of 30 shows. It returns the viewer's token, a
// show id, its second season's id and an episode id from the middle of that show.
func benchLibrary(b *testing.B, s *Server) (token string, showID, seasonID, episodeID int64) {
	b.Helper()
	words := strings.Fields("star night river the dark last of lost city king queen blue fire empire ghost house road summer winter shadow iron silver golden garden storm wild return secret ocean dream")
	people := strings.Fields("Alice Bob Carol Dave Erin Frank Grace Heidi Ivan Judy Mallory Niaj Olivia Peggy Rupert Sybil Trent Victor Walter")
	rng := rand.New(rand.NewPCG(1, 2))
	title := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = words[rng.IntN(len(words))]
		}
		w[0] = strings.ToUpper(w[0][:1]) + w[0][1:]
		return strings.Join(w, " ")
	}
	cast := func(n int) string {
		type p struct {
			Name  string `json:"name"`
			Role  string `json:"role"`
			Image string `json:"image"`
		}
		ps := make([]p, n)
		for i := range ps {
			ps[i] = p{people[rng.IntN(len(people))] + " " + people[rng.IntN(len(people))] + "son", "Character " + title(2), "https://image.tmdb.org/t/p/original/abcdefghijklmnop.jpg"}
		}
		j, _ := json.Marshal(ps)
		return string(j)
	}
	overview := strings.Repeat("A sweeping tale of ambition and loss across three generations. ", 6)
	genres := []string{`["Drama"]`, `["Action","Adventure"]`, `["Comedy"]`, `["Science Fiction","Thriller"]`}

	db := s.St.DB()
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	must := func(_ sql.Result, err error) {
		if err != nil {
			b.Fatal(err)
		}
	}
	must(tx.Exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0),(2,'TV','shows','[]',0)`))
	ins, err := tx.Prepare(`INSERT INTO items(library_id,kind,parent_id,show_id,title,sort_title,year,season,episode,overview,tagline,rating,genres,cast_json,studios,premiere,provider_ids,path,added_at,updated_at,meta_status)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`)
	if err != nil {
		b.Fatal(err)
	}
	file, err := tx.Prepare(`INSERT INTO files(item_id,library_id,path,size,mtime,added_at,duration) VALUES(?,?,?,1,0,0,?)`)
	if err != nil {
		b.Fatal(err)
	}
	add := func(lib int64, kind string, parent, show int64, t string, season, episode int, castN int) int64 {
		var p any
		if parent > 0 {
			p = parent
		}
		res, err := ins.Exec(lib, kind, p, show, t, strings.ToLower(t), 1990+rng.IntN(35), season, episode, overview, "Tagline "+t, rng.Float64()*10,
			genres[rng.IntN(len(genres))], cast(castN), `["Studio One","Studio Two"]`, fmt.Sprintf("20%02d-01-01", rng.IntN(25)),
			`{"tmdb":"12345","imdb":"tt1234567","tvdb":"76543"}`, "/media/"+t, rng.Int64N(1_000_000), 0)
		if err != nil {
			b.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if kind == "movie" || kind == "episode" {
			must(file.Exec(id, lib, fmt.Sprintf("/media/%d.mkv", id), 2400.0))
		}
		return id
	}
	for range 2000 {
		add(1, "movie", 0, 0, title(1+rng.IntN(3)), 0, 0, 20)
	}
	var shows, seasons []int64
	var eps [][]int64
	for range 60 {
		sh := add(2, "show", 0, 0, title(1+rng.IntN(3)), 0, 0, 20)
		shows = append(shows, sh)
		var se []int64
		for season := 0; season <= 5; season++ {
			n := 10
			if season == 0 {
				n = 2
			}
			sid := add(2, "season", sh, sh, fmt.Sprintf("Season %d", season), season, 0, 0)
			if season == 2 {
				seasons = append(seasons, sid)
			}
			for e := 1; e <= n; e++ {
				se = append(se, add(2, "episode", sid, sh, title(2), season, e, 8))
			}
		}
		eps = append(eps, se)
	}
	ins.Close()
	file.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	u, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		b.Fatal(err)
	}
	// Watched the first k episodes of 30 shows, one in progress on a few.
	t := int64(1)
	for i := range 30 {
		k := 3 + rng.IntN(40)
		for _, e := range eps[i][:k] {
			if err := s.St.MarkPlayed(u.ID, e, true); err != nil {
				b.Fatal(err)
			}
			must(db.Exec(`UPDATE user_data SET last_played=? WHERE user_id=? AND item_id=?`, t, u.ID, e))
			t++
		}
		if i%5 == 0 {
			must(db.Exec(`INSERT INTO user_data(user_id,item_id,position,last_played) VALUES(?,?,600,?)`, u.ID, eps[i][k], t))
			t++
		}
	}
	// Favorites: every 50th movie (ids 1-2000) and every 6th show.
	for id := int64(50); id <= 2000; id += 50 {
		if err := s.St.SetFavorite(u.ID, id, true); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < len(shows); i += 6 {
		if err := s.St.SetFavorite(u.ID, shows[i], true); err != nil {
			b.Fatal(err)
		}
	}
	token, _ = s.St.CreateToken(u.ID, "Bench", "127.0.0.1")
	return token, shows[0], seasons[0], eps[0][25]
}

// cacheServer is securityServer with a file cache, which item details need.
func cacheServer(b testing.TB) (*Server, http.Handler) {
	dir := b.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })
	log := logx.New(10, false)
	s := &Server{St: st, Log: log, Cache: cache.New(st, log, dir), Web: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("test")}}}
	h, err := s.Handler()
	if err != nil {
		b.Fatal(err)
	}
	return s, h
}

func BenchmarkLibraryEndpoints(b *testing.B) {
	s, h := cacheServer(b)
	token, show, season, ep := benchLibrary(b, s)
	for _, tc := range []struct{ name, path string }{
		{"home", "/api/home"},
		{"favorites", "/api/favorites"},
		{"items-movies-page", "/api/items?library=1&sort=title&limit=120"},
		{"items-shows", "/api/items?library=2&sort=latest&desc=1&limit=120"},
		{"search-common", "/api/search?q=star"},
		{"search-two-words", "/api/search?q=dark+river"},
		{"search-rare", "/api/search?q=empire+ghost+house"},
		{"detail-episode", fmt.Sprintf("/api/items/%d", ep)},
		{"detail-show", fmt.Sprintf("/api/items/%d", show)},
		{"season-children", fmt.Sprintf("/api/items/%d/children", season)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var size int
			for b.Loop() {
				r := httptest.NewRequest("GET", "http://lex.test"+tc.path, nil)
				r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					b.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
				}
				size = w.Body.Len()
			}
			b.ReportMetric(float64(size), "resp-bytes")
		})
	}
}

// BenchmarkCompat times the library-wide compatibility summary over the
// benchmark library's ~5,000 files, each probed as a typical MKV (one video
// stream, three audio tracks, eight subtitles).
func BenchmarkCompat(b *testing.B) {
	s, h := cacheServer(b)
	benchLibrary(b, s)
	admin, err := s.St.CreateUser("benchadmin", "test-password-123", true)
	if err != nil {
		b.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Bench", "127.0.0.1")
	info := &store.MediaInfo{Format: "matroska,webm", Duration: 2700, Bitrate: 9_000_000, Streams: []store.Stream{
		{Index: 0, Type: "video", Codec: "hevc", Profile: "Main 10", BitDepth: 10, Width: 3840, Height: 2160, HDR: "HDR10", CodecString: "hvc1.2.4.L153.B0"},
		{Index: 1, Type: "audio", Codec: "eac3", Channels: 6, Language: "eng", Default: true, CodecString: "ec-3"},
		{Index: 2, Type: "audio", Codec: "aac", Channels: 2, Language: "eng", CodecString: "mp4a.40.2"},
		{Index: 3, Type: "audio", Codec: "ac3", Channels: 6, Language: "fra", CodecString: "ac-3"},
	}}
	for i := range 8 {
		info.Streams = append(info.Streams, store.Stream{Index: 4 + i, Type: "subtitle", Codec: "subrip", Language: "eng", TextSub: true, Title: "Full"})
	}
	j, _ := json.Marshal(info)
	if _, err := s.St.DB().Exec(`UPDATE files SET info=?, path=replace(path,'.mp4','.mkv')`, string(j)); err != nil {
		b.Fatal(err)
	}
	body := `{"caps":{"mse":true,"direct":{"mp4":true},"video":{"h264":true,"hevc":true},"nativeVideo":{"h264":true},"audio":{"aac":true},"nativeAudio":{"aac":true}},"mode":"auto"}`
	for b.Loop() {
		r := httptest.NewRequest("POST", "http://lex.test/api/admin/compat", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			b.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
