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
