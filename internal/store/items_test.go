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
	// Small seeds used to leave the titles in id order.
	for seed := int64(1); seed <= 50; seed++ {
		if got := all(seed); slices.IsSorted(got) {
			t.Fatalf("seed %d gives id order: %v", seed, got)
		}
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

func TestListItemsGenreMatchesExactDecodedName(t *testing.T) {
	st := testStore(t)
	lib, err := st.CreateLibrary("Movies", "movies", []string{"/m"})
	if err != nil {
		t.Fatal(err)
	}
	add := func(title string, genres ...string) int64 {
		it := &Item{LibraryID: lib.ID, Kind: "movie", Title: title, Path: "/m/" + title}
		if _, err := st.InsertItem(it); err != nil {
			t.Fatal(err)
		}
		it.Genres = genres
		if err := st.SaveMetadata(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	scifi := add("Dune", "Sci-Fi & Fantasy", "Drama")
	drama := add("Other", "Drama")
	add("Plain")
	for _, tc := range []struct {
		genre string
		want  []int64
	}{
		{"Sci-Fi & Fantasy", []int64{scifi}},
		{"Drama", []int64{scifi, drama}},
		{"%", nil},
		{"Dram", nil},
	} {
		if got := listIDs(t, st, 1, ListQuery{Genre: tc.genre}); !slices.Equal(got, tc.want) {
			t.Errorf("genre %q = %v, want %v", tc.genre, got, tc.want)
		}
	}
	// Every listed genre must find its titles.
	genres, err := st.Genres(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range genres {
		if len(listIDs(t, st, 1, ListQuery{Genre: g})) == 0 {
			t.Errorf("genre %q lists no titles", g)
		}
	}
}

func TestSetFavoriteMissingItem(t *testing.T) {
	st := testStore(t)
	u, err := st.CreateUser("one", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetFavorite(u.ID, 9999, true); err != ErrNotFound {
		t.Fatalf("SetFavorite(missing) = %v, want ErrNotFound", err)
	}
}

func TestMarkTreePlayedDoesNotRecountWatched(t *testing.T) {
	st := testStore(t)
	db := st.DB()
	u, err := st.CreateUser("one", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'TV','shows','[]',0);
		INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES(1,1,'show',0,'S','s',0,0);
		INSERT INTO items(id,library_id,kind,parent_id,show_id,season,title,sort_title,added_at,updated_at) VALUES(2,1,'season',1,1,1,'S1','s1',0,0);
		INSERT INTO items(id,library_id,kind,parent_id,show_id,season,episode,title,sort_title,added_at,updated_at) VALUES
			(3,1,'episode',2,1,1,1,'E1','e1',0,0),(4,1,'episode',2,1,1,2,'E2','e2',0,0),(5,1,'episode',2,1,1,3,'E3','e3',0,0);
		INSERT INTO user_data(user_id,item_id,position,played,play_count,last_played) VALUES(?,3,0,1,2,100),(?,4,50,0,0,200)`, u.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkTreePlayed(u.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]UserData{3: {Played: true, PlayCount: 2, LastPlayed: 100}, 4: {Played: true, PlayCount: 1}, 5: {Played: true, PlayCount: 1}} {
		ud, err := st.GetUserData(u.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ud.Played || ud.Position != 0 || ud.PlayCount != want.PlayCount || (want.LastPlayed != 0 && ud.LastPlayed != want.LastPlayed) || ud.LastPlayed == 0 {
			t.Errorf("episode %d after marking the show = %+v, want %+v", id, ud, want)
		}
	}
	if err := st.MarkTreePlayed(u.ID, 2, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3, 4, 5} {
		if ud, _ := st.GetUserData(u.ID, id); ud.Played || ud.Position != 0 {
			t.Errorf("episode %d after unmarking the season = %+v", id, ud)
		}
	}
	if err := st.MarkTreePlayed(u.ID, 9999, true); err != ErrNotFound {
		t.Fatalf("MarkTreePlayed(missing) = %v, want ErrNotFound", err)
	}
}
