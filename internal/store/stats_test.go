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
