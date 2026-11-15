package store

import (
	"errors"
	"slices"
	"testing"
)

// browseFixture: user 1 and 2, a movie library with a trilogy (released out
// of id order) and a show library.
func browseFixture(t *testing.T) *Store {
	t.Helper()
	st := testStore(t)
	for _, name := range []string{"one", "two"} {
		if _, err := st.CreateUser(name, "test-password-123", false); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string) {
		t.Helper()
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0),(2,'TV','shows','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,year,premiere,cast_json,added_at,updated_at) VALUES
		(10,1,'movie',0,'Return','return',1983,'1983-05-25','[{"name":"Mark Hamill","role":"Luke"},{"name":"Harrison Ford","role":"Han"}]',0,0),
		(11,1,'movie',0,'Hope','hope',1977,'1977-05-25','[{"name":"Mark Hamill","role":"Luke"}]',0,0),
		(12,1,'movie',0,'Empire','empire',1980,'','[{"name":"Irvin Kershner","role":"Director"},{"name":"mark hamill","role":"Luke"}]',0,0),
		(13,1,'movie',0,'Heat','heat',1995,'1995-12-15','not json',0,0),
		(20,2,'show',0,'Corp','corp',2020,'2020-01-01','[{"name":"Mark Hamill","role":"Narrator"},{"name":"Mark Hamillton"}]',0,0)`)
	exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,episode,cast_json,added_at,updated_at) VALUES
		(21,2,'season',20,20,'Season 1','season 1',1,0,'[{"name":"Mark Hamill"}]',0,0),
		(22,2,'episode',21,20,'Pilot','pilot',1,1,'[{"name":"Mark Hamill"}]',0,0)`)
	return st
}

func TestWatchlistIsPerUserAndNewestFirst(t *testing.T) {
	st := browseFixture(t)
	for _, id := range []int64{11, 20} {
		if err := st.SetWatchlist(1, id, true); err != nil {
			t.Fatal(err)
		}
		// added_at has one-second resolution.
		st.DB().Exec(`UPDATE watchlist SET added_at=added_at-? WHERE item_id=?`, 100-id, id)
	}
	if err := st.SetWatchlist(1, 11, true); err != nil { // again: a no-op
		t.Fatal(err)
	}
	if err := st.SetWatchlist(2, 13, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWatchlist(1, 22, true); !errors.Is(err, ErrNotWatchlistable) {
		t.Fatalf("episode on watchlist: err = %v", err)
	}
	if err := st.SetWatchlist(1, 999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: err = %v", err)
	}
	wl, err := st.Watchlist(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(wl); !slices.Equal(got, []int64{20, 11}) {
		t.Fatalf("watchlist = %v, want [20 11]", got)
	}
	for _, it := range wl {
		if !it.UserData.Watchlist {
			t.Fatalf("item %d not flagged", it.ID)
		}
	}
	if got := listIDs(t, st, 1, ListQuery{LibraryID: 1, Filter: "watchlist"}); !slices.Equal(got, []int64{11}) {
		t.Fatalf("library filter = %v, want [11]", got)
	}
	if got := listIDs(t, st, 2, ListQuery{Filter: "watchlist"}); !slices.Equal(got, []int64{13}) {
		t.Fatalf("user 2 filter = %v, want [13]", got)
	}

	items, _, _ := st.ListItems(1, ListQuery{LibraryID: 1})
	if err := st.AnnotateWatchlist(1, items...); err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.UserData.Watchlist != (it.ID == 11) {
			t.Fatalf("item %d watchlist = %v", it.ID, it.UserData.Watchlist)
		}
	}

	if err := st.SetWatchlist(1, 11, false); err != nil {
		t.Fatal(err)
	}
	if wl, _ := st.Watchlist(1, 1); !slices.Equal(itemIDs(wl), []int64{20}) {
		t.Fatalf("after removal = %v, want [20]", itemIDs(wl))
	}
}

func TestCollectionsInReleaseOrder(t *testing.T) {
	st := browseFixture(t)
	sw := &Collection{ID: 10, Name: "Star Wars Collection"}
	for _, id := range []int64{10, 11, 12} {
		if err := st.SetItemCollection(id, sw); err != nil {
			t.Fatal(err)
		}
	}
	c, items, err := st.CollectionItems(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Empire has no release date: its year places it.
	if c.Name != "Star Wars Collection" || !slices.Equal(itemIDs(items), []int64{11, 12, 10}) {
		t.Fatalf("collection = %+v %v, want release order [11 12 10]", c, itemIDs(items))
	}
	if got, err := st.ItemCollection(12); err != nil || *got != *sw {
		t.Fatalf("ItemCollection = %+v, %v", got, err)
	}
	if _, err := st.ItemCollection(13); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no collection: err = %v", err)
	}

	// A rename sticks; moving every movie out drops the collection.
	if err := st.SetItemCollection(10, &Collection{ID: 10, Name: "Skywalker Saga"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.ItemCollection(11); c.Name != "Skywalker Saga" {
		t.Fatalf("renamed = %q", c.Name)
	}
	for _, id := range []int64{10, 11} {
		st.SetItemCollection(id, nil)
	}
	st.SetItemCollection(12, &Collection{ID: 99, Name: "Other"})
	if _, _, err := st.CollectionItems(1, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("emptied collection: err = %v", err)
	}
	// Deleting a movie takes it out of its collection.
	st.DB().Exec(`DELETE FROM items WHERE id=12`)
	if _, items, err := st.CollectionItems(1, 99); err != nil || len(items) != 0 {
		t.Fatalf("after delete = %v, %v", itemIDs(items), err)
	}
}

func TestPersonItemsMatchWholeNames(t *testing.T) {
	st := browseFixture(t)
	items, err := st.PersonItems(1, "Mark Hamill")
	if err != nil {
		t.Fatal(err)
	}
	// Top-level titles only, newest first; case-insensitive; not a prefix
	// match ("Mark Hamillton"); bad cast JSON is skipped.
	if got := itemIDs(items); !slices.Equal(got, []int64{20, 10, 12, 11}) {
		t.Fatalf("Mark Hamill = %v, want [20 10 12 11]", got)
	}
	if got, _ := st.PersonItems(1, "irvin kershner"); !slices.Equal(itemIDs(got), []int64{12}) {
		t.Fatalf("director = %v", itemIDs(got))
	}
	if got, _ := st.PersonItems(1, "Mark"); len(got) != 0 {
		t.Fatalf("partial name = %v", itemIDs(got))
	}
	if got, _ := st.PersonItems(1, "  "); len(got) != 0 {
		t.Fatalf("blank name = %v", itemIDs(got))
	}
}
