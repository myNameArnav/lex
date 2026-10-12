package tv

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"lex/internal/store"
)

func pool() []store.Airable {
	p := []store.Airable{{ItemID: 1, Duration: 7200}, {ItemID: 2, Duration: 5400}}
	// A show with a special and three regular episodes.
	p = append(p, store.Airable{ItemID: 10, ShowID: 9, Season: 0, Duration: 1500})
	for id := int64(11); id <= 13; id++ {
		p = append(p, store.Airable{ItemID: id, ShowID: 9, Season: 1, Duration: 1320})
	}
	return p
}

func TestScheduleFillsTheDayInOrder(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	progs := Schedule(pool(), 42, start, end)
	if len(progs) == 0 || !progs[0].Start.Equal(start) || !progs[len(progs)-1].End.Equal(end) {
		t.Fatalf("schedule doesn't cover the day: %d programs", len(progs))
	}
	durations := map[int64]time.Duration{}
	for _, a := range pool() {
		durations[a.ItemID] = time.Duration(a.Duration) * time.Second
	}
	for i, p := range progs {
		if i > 0 && !p.Start.Equal(progs[i-1].End) {
			t.Fatalf("gap or overlap before program %d", i)
		}
		if p.ItemID == 10 {
			t.Fatal("aired a special although the show has regular episodes")
		}
		// Only the last program is cut short.
		if d := p.End.Sub(p.Start); i < len(progs)-1 && d != durations[p.ItemID] {
			t.Fatalf("program %d runs %v, want %v", i, d, durations[p.ItemID])
		}
	}
	if again := Schedule(pool(), 42, start, end); !slices.Equal(progs, again) {
		t.Fatal("the same seed gave a different schedule")
	}
	if other := Schedule(pool(), 43, start, end); slices.Equal(progs, other) {
		t.Fatal("another seed gave the same schedule")
	}
}

// With three titles, the last one aired is never picked straight away (8
// redraws make a repeat vanishingly unlikely).
func TestScheduleAvoidsRepeats(t *testing.T) {
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	progs := Schedule(pool(), 7, start, start.Add(7*24*time.Hour))
	title := func(id int64) int64 {
		if id >= 10 {
			return 9
		}
		return -id
	}
	for i := 1; i < len(progs); i++ {
		if title(progs[i].ItemID) == title(progs[i-1].ItemID) {
			t.Fatalf("title %d aired twice in a row at %d", title(progs[i].ItemID), i)
		}
	}
}

func TestScheduleEmptyPool(t *testing.T) {
	start := time.Now()
	if progs := Schedule(nil, 1, start, start.Add(time.Hour)); progs != nil {
		t.Fatalf("got %d programs from nothing", len(progs))
	}
}

func TestGuideChannelsAndAt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0),(2,'Shows','shows','[]',0)`,
		`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,genres,added_at,updated_at) VALUES
			(1,1,'movie',0,'Heat','heat','["Crime"]',0,0),
			(2,1,'movie',0,'Up','up','["Family"]',0,0),
			(3,2,'show',0,'The Wire','wire','["Crime"]',0,0),
			(6,1,'movie',0,'Speed','speed','["Action \u0026 Adventure"]',0,0)`,
		`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,episode,added_at,updated_at) VALUES
			(4,2,'season',3,3,'Season 1','season 1',1,0,0,0),
			(5,2,'episode',4,3,'The Target','the target',1,1,0,0)`,
		`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at,duration) VALUES
			(1,1,1,'/m/heat.mkv',1,0,0,10200),(2,2,1,'/m/up.mkv',1,0,0,30),(3,5,2,'/tv/wire.mkv',1,0,0,3500),(4,6,1,'/m/speed.mkv',1,0,0,6000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	crime, err := st.Airables(store.AirFilter{Genres: []string{"Crime"}})
	if err != nil {
		t.Fatal(err)
	}
	// Up is too short to air; The Wire's episode has its show's genre.
	if len(crime) != 2 || crime[0].ItemID != 1 || crime[1].ItemID != 5 || crime[1].ShowID != 3 {
		t.Fatalf("Crime airables = %+v", crime)
	}
	// Stored genres are JSON, which escapes "&".
	if action, err := st.Airables(store.AirFilter{Genres: []string{"Action & Adventure"}}); err != nil || len(action) != 1 || action[0].ItemID != 6 {
		t.Fatalf("Action & Adventure airables = %+v, %v", action, err)
	}
	// Filters combine: any library, any genre, and the kind.
	for _, c := range []struct {
		f    store.AirFilter
		want []int64
	}{
		{store.AirFilter{}, []int64{1, 5, 6}},
		{store.AirFilter{Kind: "movie"}, []int64{1, 6}},
		{store.AirFilter{Kind: "show"}, []int64{5}},
		{store.AirFilter{Libraries: []int64{2}}, []int64{5}},
		{store.AirFilter{Genres: []string{"Crime", "Action & Adventure"}, Kind: "movie"}, []int64{1, 6}},
		{store.AirFilter{Libraries: []int64{1, 2}, Genres: []string{"Family"}}, nil},
	} {
		got, err := st.Airables(c.f)
		var ids []int64
		for _, a := range got {
			ids = append(ids, a.ItemID)
		}
		if err != nil || !slices.Equal(ids, c.want) {
			t.Errorf("Airables(%+v) = %v, %v; want %v", c.f, ids, err, c.want)
		}
	}
	g := New(st)
	chs, err := g.Channels()
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 3 || chs[0].ID != "all" || chs[1].ID != "lib-1" || chs[2].Number != 3 {
		t.Fatalf("channels = %+v (no genre has %d titles)", chs, minGenreTitles)
	}
	now := time.Now()
	progs, err := g.Next(chs[2], now, 2)
	if err != nil || len(progs) != 2 {
		t.Fatalf("Next: %d programs, err %v", len(progs), err)
	}
	cur, next := progs[0], progs[1]
	if cur.ItemID != 5 || cur.Start.After(now) || !cur.End.After(now) || !next.Start.Equal(cur.End) {
		t.Fatalf("now = %+v, next = %+v", cur, next)
	}
	if again, _ := g.Next(chs[2], now, 1); len(again) != 1 || again[0] != cur {
		t.Fatal("the schedule changed between two calls")
	}
	// Late in the evening, the next program comes from tomorrow's schedule.
	late := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 0, 0, now.Location())
	if progs, _ := g.Next(chs[2], late, 2); len(progs) != 2 || !progs[0].End.Equal(nextMidnight(late)) || !progs[1].Start.Equal(nextMidnight(late)) {
		t.Fatalf("across midnight: %+v", progs)
	}
}

// An admin's channels come first and air only what matches; editing one
// shows at once.
func TestCustomChannels(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db := st.DB()
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`,
		`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,genres,added_at,updated_at) VALUES
			(1,1,'movie',0,'Heat','heat','["Crime"]',0,0),(2,1,'movie',0,'Up','up','["Family"]',0,0)`,
		`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at,duration) VALUES
			(1,1,1,'/m/heat.mkv',1,0,0,6000),(2,2,1,'/m/up.mkv',1,0,0,5000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	setChannels := func(chs ...store.TVChannel) {
		c := st.Config()
		c.TVChannels = chs
		if _, err := st.SaveConfig(c); err != nil {
			t.Fatal(err)
		}
	}
	airs := func(ch Channel) map[int64]bool {
		progs, err := New(st).Programs(ch, time.Now(), time.Now().Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]bool{}
		for _, p := range progs {
			out[p.ItemID] = true
		}
		return out
	}
	g := New(st)
	setChannels(store.TVChannel{ID: "crime", Name: "Crime Night", Genres: []string{"Crime"}})
	chs, err := g.Channels()
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs[0].ID != "custom-crime" || chs[0].Name != "Crime Night" || chs[0].Number != 1 || chs[1].ID != "lib-1" {
		t.Fatalf("channels = %+v", chs)
	}
	progs, err := g.Next(chs[0], time.Now(), 3)
	if err != nil || len(progs) != 3 {
		t.Fatalf("Next: %d programs, %v", len(progs), err)
	}
	for _, p := range progs {
		if p.ItemID != 1 {
			t.Fatalf("Crime Night aired %d", p.ItemID)
		}
	}
	setChannels(store.TVChannel{ID: "crime", Name: "Family", Genres: []string{"Family"}})
	chs, _ = g.Channels()
	if chs[0].Name != "Family" {
		t.Fatalf("renamed channel = %+v", chs[0])
	}
	if progs, _ := g.Next(chs[0], time.Now(), 1); len(progs) != 1 || progs[0].ItemID != 2 {
		t.Fatalf("after editing the filter: %+v", progs)
	}
	if got := airs(chs[0]); !got[2] || got[1] {
		t.Fatalf("Family airs %v", got)
	}
	setChannels()
	if chs, _ := g.Channels(); len(chs) != 1 || chs[0].ID != "lib-1" {
		t.Fatalf("after removing it: %+v", chs)
	}
}
