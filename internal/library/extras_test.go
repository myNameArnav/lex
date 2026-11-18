package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lex/internal/store"
)

func TestExtraSuffixAndTitle(t *testing.T) {
	cases := []struct {
		name, kind, stem string
		ok               bool
	}{
		{"Heat (1995)-trailer.mkv", ExtraTrailer, "Heat (1995)", true},
		{"Heat (1995)-BehindTheScenes.mp4", ExtraBehindTheScenes, "Heat (1995)", true},
		{"Heat (1995)-sample.mkv", "", "Heat (1995)", true},
		{"Heat (1995).mkv", "", "", false},
	}
	for _, c := range cases {
		kind, stem, ok := ExtraSuffix(c.name)
		if kind != c.kind || stem != c.stem || ok != c.ok {
			t.Errorf("ExtraSuffix(%q) = %q, %q, %v", c.name, kind, stem, ok)
		}
	}
	for name, want := range map[string]string{
		"Making.of.Dune.1080p.mkv": "Making of Dune",
		"Dune-trailer.mkv":         "",
		"[Disc 2] Bloopers.mkv":    "Bloopers",
	} {
		if got := ExtraTitle(name); got != want {
			t.Errorf("ExtraTitle(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestScanIndexesExtras(t *testing.T) {
	root := t.TempDir()
	dune := filepath.Join(root, "Dune (2021)")
	os.MkdirAll(filepath.Join(dune, "Featurettes"), 0o755)
	os.MkdirAll(filepath.Join(root, "Trailers"), 0o755)
	duneFile := filepath.Join(dune, "Dune.2021.1080p.mkv")
	for _, p := range []string{
		duneFile,
		filepath.Join(dune, "Featurettes", "Making of Dune.mkv"),
		filepath.Join(dune, "Dune-trailer.mkv"),
		filepath.Join(dune, "Dune-sample.mkv"),
		// Two movies loose in one folder: only suffix-named extras, by name.
		filepath.Join(root, "Heat (1995).mkv"),
		filepath.Join(root, "Heat (1995)-trailer.mkv"),
		filepath.Join(root, "Ronin (1998).mkv"),
		// An extras folder in the library root belongs to nothing.
		filepath.Join(root, "Trailers", "Orphan.mkv"),
	} {
		mkvideo(t, p)
	}
	st, s, lib, _ := scanFixture(t, root)
	scan := func() {
		t.Helper()
		if err := s.scanLibrary(context.Background(), *lib); err != nil {
			t.Fatal(err)
		}
	}
	scan()
	movie := func(key string) *store.Item {
		t.Helper()
		it, err := st.FindItemByPath(lib.ID, "movie", key)
		if err != nil {
			t.Fatalf("movie %s: %v", key, err)
		}
		return it
	}
	extras := func(it *store.Item) map[string]string {
		t.Helper()
		ff, err := st.ItemExtras(it.ID)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, f := range ff {
			m[filepath.Base(f.Path)] = f.Extra
		}
		return m
	}
	d := movie(dune)
	if ff, _ := st.ItemFiles(d.ID); len(ff) != 1 {
		t.Fatalf("Dune has %d versions, want 1 (extras aren't versions)", len(ff))
	}
	if got := extras(d); len(got) != 2 || got["Making of Dune.mkv"] != ExtraFeaturette || got["Dune-trailer.mkv"] != ExtraTrailer {
		t.Fatalf("Dune extras = %v", got)
	}
	if got := extras(movie(root + "#heat1995")); len(got) != 1 || got["Heat (1995)-trailer.mkv"] != ExtraTrailer {
		t.Fatalf("Heat extras = %v", got)
	}
	if got := extras(movie(root + "#ronin1998")); len(got) != 0 {
		t.Fatalf("Ronin extras = %v", got)
	}
	if movies, _ := st.ItemsByKind(lib.ID, "movie"); len(movies) != 3 {
		t.Fatalf("%d movies, want 3", len(movies))
	}
	stats, err := st.LibraryStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 {
		t.Fatalf("library stats count %d files, want 3 (no extras)", stats.Files)
	}

	// Rescanning changes nothing; the movie going takes its extras with it.
	scan()
	if got := extras(movie(dune)); len(got) != 2 {
		t.Fatalf("after rescan Dune extras = %v", got)
	}
	os.Remove(duneFile)
	scan()
	if _, err := st.FindItemByPath(lib.ID, "movie", dune); err == nil {
		t.Fatal("Dune kept by its extras")
	}
	if files, _ := st.LibraryFiles(lib.ID); len(files) != 3 {
		t.Fatalf("%d files left, want Heat, its trailer and Ronin", len(files))
	}
}

func TestScanAttachesShowExtrasToTheShow(t *testing.T) {
	root := t.TempDir()
	show := filepath.Join(root, "Severance")
	os.MkdirAll(filepath.Join(show, "Season 1", "Trailers"), 0o755)
	os.MkdirAll(filepath.Join(show, "Extras"), 0o755)
	for _, p := range []string{
		filepath.Join(show, "Season 1", "Severance S01E01.mkv"),
		filepath.Join(show, "Season 1", "Trailers", "Season 1 Trailer.mkv"),
		filepath.Join(show, "Extras", "Bloopers.mkv"),
	} {
		mkvideo(t, p)
	}
	st, s, _, _ := scanFixture(t, t.TempDir())
	lib, err := st.CreateLibrary("Shows", "shows", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	it, err := st.FindItemByPath(lib.ID, "show", show)
	if err != nil {
		t.Fatal(err)
	}
	ff, _ := st.ItemExtras(it.ID)
	if len(ff) != 2 {
		t.Fatalf("show has %d extras, want 2", len(ff))
	}
	eps, _ := st.ItemsByKind(lib.ID, "episode")
	if len(eps) != 1 {
		t.Fatalf("%d episodes, want 1 (extras aren't episodes)", len(eps))
	}
}
