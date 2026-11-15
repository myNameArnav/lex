package store

import (
	"path/filepath"
	"slices"
	"testing"
)

// Users from before per-user library access keep seeing every library.
func TestLibraryAccessMigrationKeepsEveryoneUnrestricted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	// An install from before: no all_libraries column, no user_libraries.
	for _, q := range []string{`DROP TABLE user_libraries`, `ALTER TABLE users DROP COLUMN all_libraries`} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if st, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.User(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllLibraries || len(got.Libraries) != 0 || !got.CanAccess(42) {
		t.Fatalf("migrated user: %+v", got)
	}
}

func TestLibraryAccessFiltersItemQueries(t *testing.T) {
	st := testStore(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'A','movies','[]',0),(2,'B','movies','[]',0)`)
	exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,genres,added_at,updated_at) VALUES(1,1,'movie',0,'A','a','["X"]',0,0),(2,2,'movie',0,'B','b','["Y"]',0,0)`)
	admin, _ := st.CreateUser("admin", "test-password-123", true)
	kid, _ := st.CreateUser("kid", "test-password-123", false)
	if err := st.SetLibraryAccess(kid.ID, false, []int64{1, 77}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLibraryAccess(admin.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLibraryAccess(999, true, nil); err != ErrNotFound {
		t.Fatalf("unknown user: %v", err)
	}
	k, _ := st.User(kid.ID)
	if k.AllLibraries || !slices.Equal(k.Libraries, []int64{1}) || !k.CanAccess(1) || k.CanAccess(2) {
		t.Fatalf("kid: %+v", k)
	}
	for _, tc := range []struct {
		uid  int64
		want []int64
	}{{kid.ID, []int64{1}}, {admin.ID, []int64{1, 2}}, {0, []int64{1, 2}}, {12345, []int64{}}} {
		if got := listIDs(t, st, tc.uid, ListQuery{}); !slices.Equal(got, tc.want) {
			t.Errorf("uid %d lists %v, want %v", tc.uid, got, tc.want)
		}
	}
	if _, err := st.ItemForUser(2, kid.ID); err != ErrNotFound {
		t.Fatalf("kid got item 2: %v", err)
	}
	if m, _ := st.ItemsForUser([]int64{1, 2}, kid.ID); len(m) != 1 || m[1] == nil {
		t.Fatalf("ItemsForUser: %v", m)
	}
	if g, _ := st.Genres(kid.ID, 0); !slices.Equal(g, []string{"X"}) {
		t.Fatalf("genres: %v", g)
	}
	// Back to all libraries clears the list.
	if err := st.SetLibraryAccess(kid.ID, true, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if k, _ = st.User(kid.ID); !k.AllLibraries || len(k.Libraries) != 0 {
		t.Fatalf("kid, all: %+v", k)
	}
}
