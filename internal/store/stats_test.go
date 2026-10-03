package store

import (
	"testing"
	"time"
)

func TestPlaybackStatsGroupsShowsAndUsesViewerTimeZone(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'TV','shows','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES
		(1,1,'show',0,'The Office','the office',0,0),
		(2,1,'episode',1,'Pilot','pilot',0,0),
		(3,1,'episode',1,'Diversity Day','diversity day',0,0),
		(4,1,'movie',0,'Dune','dune',0,0)`)
	// 20:30 UTC yesterday; 02:00 the next day in UTC+5:30.
	y := time.Now().UTC().AddDate(0, 0, -1)
	at := time.Date(y.Year(), y.Month(), y.Day(), 20, 30, 0, 0, time.UTC).Unix()
	for _, it := range []struct {
		item  int
		title string
	}{{2, "The Office"}, {3, "The Office"}, {4, "Dune"}} {
		exec(`INSERT INTO history(user_id,item_id,title,started_at,ended_at,watched) VALUES(1,?,?,?,?,600)`, it.item, it.title, at, at+600)
	}
	ps, err := st.PlaybackStats(30, 330)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.TopItems) != 2 || ps.TopItems[0].Key != "The Office" || ps.TopItems[0].Count != 2 {
		t.Fatalf("top items = %+v, want The Office twice then Dune", ps.TopItems)
	}
	if ps.UniqueItems != 2 {
		t.Fatalf("unique titles = %d, want 2", ps.UniqueItems)
	}
	if len(ps.Hours24) != 1 || ps.Hours24[0].Key != "02" {
		t.Fatalf("hours of day = %+v, want 02 local", ps.Hours24)
	}
}

func TestLibraryTitlesFindsAnyAudioTrackAndGroupsShows(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'All','mixed','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,year,added_at,updated_at) VALUES
		(1,1,'show',0,'Silo','silo',2023,0,0),
		(2,1,'episode',1,'Freedom Day','freedom day',0,0,0),
		(3,1,'episode',1,'Holston''s Pick','holston',0,0,0),
		(4,1,'movie',0,'Dune','dune',2021,0,0)`)
	info := func(codecs ...string) string {
		s := `{"streams":[{"type":"video","codec":"hevc"}`
		for _, c := range codecs {
			s += `,{"type":"audio","codec":"` + c + `"}`
		}
		return s + `]}`
	}
	exec(`INSERT INTO files(item_id,library_id,path,size,mtime,acodec,hdr,info,added_at) VALUES
		(2,1,'/m/Silo/e1.mkv',100,0,'eac3','HDR10',?,0),
		(3,1,'/m/Silo/e2.mkv',200,0,'eac3','HDR10',?,0),
		(4,1,'/m/Dune.mp4',500,0,'aac','',?,0)`, info("eac3", "dts"), info("eac3", "dts"), info("aac"))
	dts, err := st.LibraryTitles("audio", "dts")
	if err != nil {
		t.Fatal(err)
	}
	if len(dts) != 1 || dts[0].Title != "Silo" || dts[0].Kind != "show" || dts[0].Files != 2 || dts[0].Bytes != 300 {
		t.Fatalf("dts titles = %+v, want Silo with 2 episodes", dts)
	}
	hdr, _ := st.LibraryTitles("hdr", "SDR")
	if len(hdr) != 1 || hdr[0].Title != "Dune" {
		t.Fatalf("SDR titles = %+v, want Dune", hdr)
	}
	mp4, _ := st.LibraryTitles("container", "MP4")
	if len(mp4) != 1 || mp4[0].Title != "Dune" {
		t.Fatalf("mp4 titles = %+v, want Dune", mp4)
	}
	ls, err := st.LibraryStats()
	if err != nil {
		t.Fatal(err)
	}
	var dtsFiles int
	for _, b := range ls.AudioCodecs {
		if b.Key == "dts" {
			dtsFiles = b.Count
		}
	}
	if dtsFiles != 2 {
		t.Fatalf("audio buckets = %+v, want dts in 2 files", ls.AudioCodecs)
	}
	if _, err := st.LibraryTitles("nope", "x"); err == nil {
		t.Fatal("unknown category accepted")
	}
}

func TestPlaybackStatsCountsEachConversionReason(t *testing.T) {
	st := testStore(t)
	at := time.Now().Add(-time.Hour).Unix()
	for _, r := range []string{
		"container mkv not supported",
		"container mkv not supported; video codec hevc10 not supported",
		"container mkv not supported; video codec hevc10 not supported; audio codec ac3 not supported",
		"bitrate 12000 kbps over 8000 kbps limit",
		"bitrate 9000 kbps over 4000 kbps limit; container mkv not supported",
		"",
	} {
		if _, err := st.DB().Exec(`INSERT INTO history(user_id,item_id,title,started_at,ended_at,watched,reasons) VALUES(1,1,'X',?,?,60,?)`, at, at+60, r); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := st.PlaybackStats(30, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, b := range ps.Reasons {
		got[b.Key] = b.Count
	}
	want := map[string]int{
		"container mkv not supported":      4,
		"video codec hevc10 not supported": 2,
		"audio codec ac3 not supported":    1,
		"bitrate over the limit":           2,
	}
	if len(got) != len(want) {
		t.Fatalf("reasons = %+v, want %v", ps.Reasons, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("reason %q counted %d times, want %d (all: %+v)", k, got[k], n, ps.Reasons)
		}
	}
	if ps.Reasons[0].Key != "container mkv not supported" {
		t.Fatalf("reasons not sorted by count: %+v", ps.Reasons)
	}
}

func TestHistoryNamesTheEpisode(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'TV','shows','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,season,episode,episode_end,added_at,updated_at) VALUES
		(1,1,'show',0,'Severance','severance',0,0,0,0,0),
		(2,1,'episode',1,'In Perpetuity','in perpetuity',1,3,0,0,0),
		(3,1,'episode',1,'Double','double',2,1,2,0,0),
		(4,1,'movie',0,'Dune','dune',0,0,0,0,0)`)
	for i, it := range []struct {
		item  int
		title string
	}{{2, "Severance"}, {3, "Severance"}, {4, "Dune"}, {99, "Deleted"}} {
		exec(`INSERT INTO history(user_id,item_id,title,started_at,ended_at,watched) VALUES(1,?,?,?,?,60)`, it.item, it.title, 1000-i, 1060-i)
	}
	h, err := st.History(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"S1 E3 · In Perpetuity", "S2 E1–2 · Double", "", ""}
	if len(h) != len(want) {
		t.Fatalf("got %d rows, want %d", len(h), len(want))
	}
	for i, w := range want {
		if h[i].Subtitle != w {
			t.Fatalf("row %d (%s) subtitle = %q, want %q", i, h[i].Title, h[i].Subtitle, w)
		}
	}
}
