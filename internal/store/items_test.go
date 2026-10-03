package store

import (
	"slices"
	"testing"
)

func listIDs(t *testing.T, st *Store, uid int64, q ListQuery) []int64 {
	t.Helper()
	items, _, err := st.ListItems(uid, q)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func TestListItemsShowsUseEpisodeActivity(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"one", "two"} {
		if _, err := st.CreateUser(name, "test-password-123", false); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'TV','shows','[]',0),(2,'Movies','movies','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES
		(1,1,'show',0,'Alpha','alpha',0,0),
		(4,1,'show',0,'Bravo','bravo',0,0),
		(7,1,'show',0,'Charlie','charlie',0,0),
		(9,1,'show',0,'Delta','delta',0,0),
		(20,2,'movie',0,'Movie One','movie one',0,0),
		(21,2,'movie',0,'Movie Two','movie two',0,0)`)
	exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,added_at,updated_at) VALUES
		(2,1,'episode',1,1,'A1','a1',0,0),
		(3,1,'episode',1,1,'A2','a2',0,0),
		(5,1,'episode',4,4,'B1','b1',0,0),
		(6,1,'episode',4,4,'B2','b2',0,0),
		(8,1,'episode',7,7,'C1','c1',0,0),
		(10,1,'episode',9,9,'D1','d1',0,0)`)
	// Alpha: E1 watched, E2 not started. Bravo: E1 half watched. Charlie:
	// fully watched. Delta: untouched. Another user's activity must not count.
	exec(`INSERT INTO user_data(user_id,item_id,position,played,last_played) VALUES
		(1,2,0,1,100),
		(1,5,300,0,200),
		(1,8,0,1,50),
		(2,10,120,0,999),
		(1,21,60,0,10)`)

	if got := listIDs(t, st, 1, ListQuery{LibraryID: 1, Filter: "inprogress"}); !slices.Equal(got, []int64{1, 4}) {
		t.Fatalf("in progress shows = %v, want [1 4]", got)
	}
	if got := listIDs(t, st, 2, ListQuery{LibraryID: 1, Filter: "inprogress"}); !slices.Equal(got, []int64{9}) {
		t.Fatalf("other user's in progress shows = %v, want [9]", got)
	}
	if got := listIDs(t, st, 1, ListQuery{LibraryID: 1, Sort: "played", Desc: true}); !slices.Equal(got, []int64{4, 1, 7, 9}) {
		t.Fatalf("recently watched shows = %v, want [4 1 7 9]", got)
	}
	// Movies keep using their own user data.
	if got := listIDs(t, st, 1, ListQuery{LibraryID: 2, Filter: "inprogress"}); !slices.Equal(got, []int64{21}) {
		t.Fatalf("in progress movies = %v, want [21]", got)
	}
	if got := listIDs(t, st, 1, ListQuery{LibraryID: 2, Sort: "played", Desc: true}); !slices.Equal(got, []int64{21, 20}) {
		t.Fatalf("recently watched movies = %v, want [21 20]", got)
	}
}

func TestListItemsSeededRandomIsStableAcrossPages(t *testing.T) {
	st := testStore(t)
	if _, err := st.DB().Exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 30; i++ {
		if _, err := st.DB().Exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES(?,1,'movie',0,'M','m',0,0)`, i); err != nil {
			t.Fatal(err)
		}
	}
	all := func(seed int64) []int64 {
		var out []int64
		for off := 0; off < 30; off += 7 {
			out = append(out, listIDs(t, st, 1, ListQuery{LibraryID: 1, Sort: "random", Seed: seed, Limit: 7, Offset: off})...)
		}
		return out
	}
	a := all(12345)
	sorted := slices.Clone(a)
	slices.Sort(sorted)
	if len(slices.Compact(sorted)) != 30 || sorted[0] != 1 || sorted[29] != 30 {
		t.Fatalf("pages must cover every title once: %v", a)
	}
	if !slices.Equal(a, all(12345)) {
		t.Fatal("the same seed must give the same order")
	}
	if slices.IsSorted(a) || slices.Equal(a, all(999)) {
		t.Fatalf("seeded order should be shuffled and depend on the seed: %v", a)
	}
	// Seeds of any size stay within 64-bit arithmetic.
	for _, seed := range []int64{-5, 1 << 62, -1 << 63} {
		if got := all(seed); len(got) != 30 {
			t.Fatalf("seed %d: %d items", seed, len(got))
		}
	}
}

func TestListItemsSearchMatchesWildcardsLiterally(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	if _, err := db.Exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES
		(1,1,'movie',0,'Plain Title','plain title',0,0),
		(2,1,'movie',0,'100% Cotton','100% cotton',0,0),
		(3,1,'movie',0,'snake_case','snake_case',0,0),
		(4,1,'movie',0,'Back\slash','back\slash',0,0)`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q    string
		want []int64
	}{
		{"%", []int64{2}},
		{"_", []int64{3}},
		{`\`, []int64{4}},
		{"0% c", []int64{2}},
		{"TITLE", []int64{1}},
	} {
		if got := listIDs(t, st, 1, ListQuery{Search: tc.q}); !slices.Equal(got, tc.want) {
			t.Errorf("search %q = %v, want %v", tc.q, got, tc.want)
		}
	}
}
