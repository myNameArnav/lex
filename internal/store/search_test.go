package store

import (
	"path/filepath"
	"slices"
	"testing"
)

func searchFixture(t *testing.T, st *Store) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Everything','mixed','[]',0)`,
		`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,original_title,genres,cast_json,added_at,updated_at) VALUES
			(1,1,'movie',0,'Amélie','amélie','Le Fabuleux Destin d''Amélie Poulain','["Comedy","Romance"]','[{"name":"Audrey Tautou","role":"Amélie"}]',0,0),
			(2,1,'movie',0,'Spider-Man','spider-man','','["Action"]','[{"name":"Tobey Maguire"}]',0,0),
			(3,1,'movie',0,'Starman','starman','','["Science Fiction"]','[{"name":"Jeff Bridges"}]',0,0),
			(4,1,'movie',0,'The Big Lebowski','big lebowski','','["Comedy"]','[{"name":"Jeff Bridges"},{"name":"John Goodman"}]',0,0),
			(5,1,'movie',0,'Jeff, Who Lives at Home','jeff, who lives at home','','["Comedy"]','[]',0,0),
			(6,1,'movie',0,'Broken JSON','broken json','','not json','{"name":1}',0,0),
			(7,1,'show',0,'Ocean''s Deep','ocean''s deep','','["Documentary"]','[]',0,0)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSearchIndex(t *testing.T) {
	st := testStore(t)
	searchFixture(t, st)
	for _, tc := range []struct {
		q    string
		want []int64
	}{
		{"amelie", []int64{1}},           // accents folded
		{"AMÉL", []int64{1}},             // prefix, any case
		{"poulain", []int64{1}},          // original title
		{"spider man", []int64{2}},       // words in any form
		{"spider-man", []int64{2}},       // a phrase
		{"man spider", []int64{2}},       // any order
		{"star", []int64{3}},             // prefix of a word, not inside one: not "Spider-Man"
		{"tautou", []int64{1}},           // cast
		{"romance", []int64{1}},          // genre
		{"jeff", []int64{5, 4, 3}},       // the title match first, then by title
		{"goodman lebowski", []int64{4}}, // cast and title together
		{"ocean's", []int64{7}},
		{"oceans", nil},        // no word match: falls back to substring, which "Ocean's" isn't either...
		{"pider", []int64{2}},  // ...or a substring match inside a word
		{"broken", []int64{6}}, // invalid JSON doesn't break indexing
		{`"star" OR jeff`, nil},
	} {
		if got := listIDs(t, st, 0, ListQuery{Search: tc.q}); !slices.Equal(got, tc.want) {
			t.Errorf("search %q = %v, want %v", tc.q, got, tc.want)
		}
	}
	// Totals follow the same match, and paging keeps the title-first order.
	if _, total, _ := st.ListItems(0, ListQuery{Search: "jeff"}); total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if got := listIDs(t, st, 0, ListQuery{Search: "jeff", Limit: 1, Offset: 1}); !slices.Equal(got, []int64{4}) {
		t.Fatalf("page 2 = %v, want [4]", got)
	}
	// Kind and sort still apply.
	if got := listIDs(t, st, 0, ListQuery{Search: "jeff", Sort: "title", Desc: true}); !slices.Equal(got, []int64{5, 3, 4}) {
		t.Fatalf("jeff by title desc = %v, want [5 3 4]", got)
	}
	if got := listIDs(t, st, 0, ListQuery{Search: "o", Kind: "show"}); !slices.Equal(got, []int64{7}) {
		t.Fatalf("shows = %v", got)
	}
}

func TestSearchIndexFollowsItems(t *testing.T) {
	st := testStore(t)
	searchFixture(t, st)
	it, err := st.Item(3)
	if err != nil {
		t.Fatal(err)
	}
	it.Title, it.SortTitle = "Tron", ""
	it.Cast = []Person{{Name: "Kurt Russell"}}
	if err := st.SaveMetadata(it); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, st, 0, ListQuery{Search: "starman"}); len(got) != 0 {
		t.Fatalf("old title still found: %v", got)
	}
	if got := listIDs(t, st, 0, ListQuery{Search: "tron russell"}); !slices.Equal(got, []int64{3}) {
		t.Fatalf("new title and cast = %v", got)
	}
	if err := st.SetItemStructure(2, "Spider-Verse", 0, 0, 0, 0, Hint{}); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, st, 0, ListQuery{Search: "verse"}); !slices.Equal(got, []int64{2}) {
		t.Fatalf("restructured title = %v", got)
	}
	// Deleting a library deletes its items' index rows too.
	if err := st.DeleteLibrary(1); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM items_fts`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d index rows left after deleting the library", n)
	}
}

func TestSearchIndexIsRebuiltOnOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	searchFixture(t, st)
	// An index that lost rows (or a database from before it existed).
	if _, err := st.DB().Exec(`DELETE FROM items_fts WHERE rowid IN (1,2)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := listIDs(t, st, 0, ListQuery{Search: "spider"}); !slices.Equal(got, []int64{2}) {
		t.Fatalf("after reopening: %v", got)
	}
	// A database from before the index: no table, no version.
	if _, err := st.DB().Exec(`DROP TABLE items_fts; DELETE FROM kv WHERE key='search_index'`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := listIDs(t, st, 0, ListQuery{Search: "tautou"}); !slices.Equal(got, []int64{1}) {
		t.Fatalf("after re-creating the index: %v", got)
	}
}

func TestFTSQuery(t *testing.T) {
	for in, want := range map[string]string{
		"":               "",
		"%":              "",
		"  _ \\ ":        "",
		"Star wars":      `"Star"* "wars"*`,
		`say "hi"`:       `"say"* """hi"""*`,
		"fast & furious": `"fast"* "furious"*`,
		"0% c":           `"0%"* "c"*`,
		"NOT":            `"NOT"*`,
	} {
		if got := ftsQuery(in); got != want {
			t.Errorf("ftsQuery(%q) = %s, want %s", in, got, want)
		}
	}
}

// List responses carry card fields only; detail keeps everything.
func TestListProjections(t *testing.T) {
	st := testStore(t)
	searchFixture(t, st)
	if _, err := st.DB().Exec(`UPDATE items SET overview='Long text',studios='["S"]',provider_ids='{"tmdb":"1"}',tagline='T',premiere='2001-04-25',rating=7.5,year=2001`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,overview,premiere,cast_json,added_at,updated_at) VALUES
		(8,1,'season',7,7,'Season 1','season 1',1,'Season text','2001-01-01','[{"name":"X"}]',0,0)`); err != nil {
		t.Fatal(err)
	}
	items, _, err := st.ListItems(0, ListQuery{Search: "amelie"})
	if err != nil || len(items) != 1 {
		t.Fatalf("%v %v", items, err)
	}
	c := items[0]
	if c.Title != "Amélie" || c.Year != 2001 || c.UserData == nil {
		t.Fatalf("card lost a field it shows: %+v", c)
	}
	if c.Overview != "" || c.Cast != nil || c.Studios != nil || c.ProviderIDs != nil || c.Genres != nil || c.Tagline != "" || c.OriginalTitle != "" || c.Rating != 0 {
		t.Fatalf("card has detail fields: %+v", c)
	}
	ch, err := st.ChildCards(7, 0)
	if err != nil || len(ch) != 1 {
		t.Fatalf("%v %v", ch, err)
	}
	if ch[0].Overview != "Season text" || ch[0].Premiere != "2001-01-01" || ch[0].Cast != nil {
		t.Fatalf("child card = %+v", ch[0])
	}
	full, err := st.ItemForUser(1, 0)
	if err != nil || full.Overview == "" || len(full.Cast) != 1 || full.ProviderIDs["tmdb"] != "1" {
		t.Fatalf("detail = %+v (%v)", full, err)
	}
	// Internal callers of Children keep full items.
	if all, _ := st.Children(7, 0); len(all) != 1 || len(all[0].Cast) != 1 {
		t.Fatalf("Children = %+v", all)
	}
}
