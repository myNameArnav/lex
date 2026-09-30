package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

func TestScanKeepsItemsInUnreadableFolders(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	root := t.TempDir()
	season := filepath.Join(root, "Silo (2023)", "Season 01")
	if err := os.MkdirAll(season, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Silo - S01E01 - Freedom Day.mkv", "Silo - S01E02 - Holston's Pick.mkv"} {
		mkvideo(t, filepath.Join(season, name))
	}
	movie := filepath.Join(root, "Dune (2021)")
	os.MkdirAll(movie, 0o755)
	mkvideo(t, filepath.Join(movie, "Dune.2021.mkv"))

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, err := st.CreateLibrary("Mixed", "mixed", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	s := NewScanner(st, "ffprobe", logx.New(50, false))
	count := func() int {
		files, err := st.LibraryFiles(lib.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(files)
	}
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Fatalf("first scan found %d files, want 3", n)
	}

	// The season folder becomes unreadable (permissions, flaky share).
	if err := os.Chmod(season, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(season, 0o755)
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 3 {
		t.Fatalf("after the folder became unreadable: %d files, want all 3 kept", n)
	}

	// Really deleting a file still removes it.
	os.Chmod(season, 0o755)
	os.Remove(filepath.Join(movie, "Dune.2021.mkv"))
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("after deleting a file: %d files, want 2", n)
	}
}

// A second version of a movie next to the first must not change the movie's
// item: that used to re-key it, and pruning the old item dropped watch
// progress, favorites, intro segments and a manual match.
func TestScanKeepsMovieItemAcrossVersions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Dune (2021)")
	os.MkdirAll(dir, 0o755)
	mkvideo(t, filepath.Join(dir, "Dune.2021.1080p.mkv"))

	st, s, lib, uid := scanFixture(t, root)
	scan := func() {
		t.Helper()
		if err := s.scanLibrary(context.Background(), *lib); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	movie, err := st.FindItemByPath(lib.ID, "movie", dir)
	if err != nil {
		t.Fatal(err)
	}
	st.SaveProgress(uid, movie.ID, 1234)
	st.SetFavorite(uid, movie.ID, true)

	check := func(stage string, files int) {
		t.Helper()
		it, err := st.FindItemByPath(lib.ID, "movie", dir)
		if err != nil {
			t.Fatalf("%s: movie item gone: %v", stage, err)
		}
		if it.ID != movie.ID {
			t.Fatalf("%s: movie is item %d, want %d", stage, it.ID, movie.ID)
		}
		if ff, _ := st.ItemFiles(it.ID); len(ff) != files {
			t.Fatalf("%s: item has %d files, want %d", stage, len(ff), files)
		}
		ud, _ := st.GetUserData(uid, it.ID)
		if ud.Position != 1234 || !ud.Favorite {
			t.Fatalf("%s: user data lost: %+v", stage, ud)
		}
		movies, _ := st.ItemsByKind(lib.ID, "movie")
		if len(movies) != 1 {
			t.Fatalf("%s: %d movie items, want 1", stage, len(movies))
		}
	}

	second := filepath.Join(dir, "Dune.2021.2160p.HDR.mkv")
	mkvideo(t, second)
	scan()
	check("second version added", 2)

	os.Remove(second)
	scan()
	check("second version removed", 1)
}

// When a file really moves to another item (here: an unrelated film shares
// its folder), watch state and a manual match go with it.
func TestScanCarriesDataWhenFileChangesItem(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Films")
	os.MkdirAll(dir, 0o755)
	mkvideo(t, filepath.Join(dir, "Heat.1995.mkv"))

	st, s, lib, uid := scanFixture(t, root)
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	old, err := st.FindItemByPath(lib.ID, "movie", dir)
	if err != nil {
		t.Fatal(err)
	}
	st.MarkPlayed(uid, old.ID, true)
	old.ProviderIDs = map[string]string{"tmdb": "949", "source": "tmdb"}
	old.MetaLocked, old.MetaStatus, old.Overview = true, store.MetaMatched, "A heist."
	if err := st.SaveMetadata(old); err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`INSERT INTO segments(item_id,kind,start,end,source) VALUES(?,?,?,?,?)`, old.ID, "credits", 100, 200, "chapters")

	mkvideo(t, filepath.Join(dir, "Ronin.1998.mkv"))
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Item(old.ID); err == nil {
		t.Fatal("folder item should have been pruned")
	}
	heat, err := st.FindItemByPath(lib.ID, "movie", dir+"#heat1995")
	if err != nil {
		t.Fatal(err)
	}
	if ud, _ := st.GetUserData(uid, heat.ID); !ud.Played {
		t.Fatalf("watched state lost: %+v", ud)
	}
	if !heat.MetaLocked || heat.ProviderIDs["tmdb"] != "949" || heat.Overview != "A heist." || heat.MetaStatus != store.MetaMatched {
		t.Fatalf("manual match lost: locked=%v ids=%v status=%d", heat.MetaLocked, heat.ProviderIDs, heat.MetaStatus)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM segments WHERE item_id=?`, heat.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("segments lost: %d", n)
	}
	if ronin, err := st.FindItemByPath(lib.ID, "movie", dir+"#ronin1998"); err != nil || ronin.MetaLocked {
		t.Fatalf("ronin: %v %+v", err, ronin)
	}
}

func TestMovieFolders(t *testing.T) {
	e := func(p string) entry { return entry{path: p} }
	got := movieFolders([]entry{
		e("/m/Dune (2021)/Dune.2021.1080p.mkv"), e("/m/Dune (2021)/Dune.2021.2160p.mkv"),
		e("/m/Alien (1979)/Alien.mkv"), e("/m/Alien (1979)/Alien (1979) - Director's Cut.mkv"),
		e("/m/Aliens (1986)/Aliens.1986.mkv"), e("/m/Aliens (1986)/Alien Resurrection.mkv"),
		e("/m/Heat/Heat.1995.mkv"), e("/m/Heat/Heat.4K.mkv"),
		e("/m/Films/Heat.1995.mkv"), e("/m/Films/Ronin.1998.mkv"),
		e("/m/Remakes/Suspiria.1977.mkv"), e("/m/Remakes/Suspiria.2018.mkv"),
		e("/m/Solo/anything.mkv"),
	})
	for dir, want := range map[string]bool{
		"/m/Dune (2021)": true, "/m/Alien (1979)": true, "/m/Heat": true, "/m/Solo": true,
		"/m/Films": false, "/m/Remakes": false, "/m/Aliens (1986)": false,
	} {
		if (got[dir] != nil) != want {
			t.Errorf("%s: one movie=%v, want %v", dir, got[dir] != nil, want)
		}
	}
	if md := got["/m/Heat"]; md == nil || md.title != "Heat" || md.year != 1995 {
		t.Errorf("Heat folder: %+v", md)
	}
}

func scanFixture(t *testing.T, root string) (*store.Store, *Scanner, *store.Library, int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib, err := st.CreateLibrary("Movies", "movies", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("viewer", "correct horse battery staple", false)
	if err != nil {
		t.Fatal(err)
	}
	return st, NewScanner(st, "ffprobe", logx.New(50, false)), lib, u.ID
}

// mkvideo creates a sparse file big enough not to count as a sample.
func mkvideo(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(2 << 20); err != nil {
		t.Fatal(err)
	}
}
