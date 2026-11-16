package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

// scanEpisodes scans lib and returns its episodes by title.
func scanEpisodes(t *testing.T, st *store.Store, s *Scanner, lib *store.Library) map[string]*store.Item {
	t.Helper()
	if err := s.scanLibrary(context.Background(), *lib); err != nil {
		t.Fatal(err)
	}
	eps, err := st.ItemsByKind(lib.ID, "episode")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*store.Item{}
	for _, e := range eps {
		out[e.Title] = e
	}
	return out
}

func newLib(t *testing.T, kind, root string) (*store.Store, *Scanner, *store.Library) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lib, err := st.CreateLibrary("Lib", kind, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	return st, NewScanner(st, "ffprobe", logx.New(50, false)), lib
}

// Date-named episodes of a daily show are grouped by year, ordered by date
// and keep their air date.
func TestScanDailyShow(t *testing.T) {
	root := t.TempDir()
	show := filepath.Join(root, "The Daily Show")
	os.MkdirAll(filepath.Join(show, "Season 2024"), 0o755)
	mkvideo(t, filepath.Join(show, "Season 2024", "The.Daily.Show.2024.03.05.Guest.Name.1080p.WEB.mkv"))
	mkvideo(t, filepath.Join(show, "Season 2024", "The Daily Show - 2024-01-10 - Other Guest.mkv"))
	mkvideo(t, filepath.Join(show, "The Daily Show - 2023-12-31.mkv"))
	st, s, lib := newLib(t, "shows", root)

	eps := scanEpisodes(t, st, s, lib)
	if len(eps) != 3 {
		t.Fatalf("got %d episodes, want 3: %v", len(eps), eps)
	}
	for _, want := range []struct {
		title, date string
		season, ep  int
	}{
		{"Guest Name", "2024-03-05", 2024, 305},
		{"Other Guest", "2024-01-10", 2024, 110},
		{"2023-12-31", "2023-12-31", 2023, 1231},
	} {
		e := eps[want.title]
		if e == nil {
			t.Errorf("no episode %q", want.title)
			continue
		}
		if e.Premiere != want.date || e.Season != want.season || e.Episode != want.ep {
			t.Errorf("%q: premiere %q S%dE%d, want %q S%dE%d", want.title, e.Premiere, e.Season, e.Episode, want.date, want.season, want.ep)
		}
		if e.Path != filepath.Join(root, "The Daily Show")+"|D"+want.date {
			t.Errorf("%q: key %q", want.title, e.Path)
		}
		season, err := st.Item(e.ParentID)
		if err != nil || season.Season != want.season || season.Title != fmt.Sprintf("Season %d", want.season) {
			t.Errorf("%q: season %+v %v", want.title, season, err)
		}
	}

	// Once the metadata agent placed an episode in its provider's season,
	// rescans leave it there and don't bring back the empty year.
	e := eps["Guest Name"]
	if ok, err := st.PlaceEpisode(e.ID, 29, 45); !ok || err != nil {
		t.Fatalf("PlaceEpisode: %v %v", ok, err)
	}
	eps = scanEpisodes(t, st, s, lib)
	if got := eps["Guest Name"]; got == nil || got.ID != e.ID || got.Season != 29 || got.Episode != 45 {
		t.Fatalf("after rescan: %+v", got)
	}
	seasons, _ := st.ItemsByKind(lib.ID, "season")
	nums := map[int]bool{}
	for _, se := range seasons {
		nums[se.Season] = true
	}
	if !nums[29] || !nums[2024] || !nums[2023] || len(seasons) != 3 {
		t.Errorf("seasons after placing one episode: %v", nums)
	}
}

// In a Mixed library a date-named file outside a Season folder is an
// episode, its show named by the text before the date; films with only a
// year stay movies.
func TestScanMixedDailyEpisodes(t *testing.T) {
	root := t.TempDir()
	mkvideo(t, filepath.Join(root, "The.Daily.Show.2024.03.05.Guest.Name.1080p.WEB.mkv"))
	mkvideo(t, filepath.Join(root, "Show - 2024-03-06 - Title.mkv"))
	os.MkdirAll(filepath.Join(root, "Dune (2021)"), 0o755)
	mkvideo(t, filepath.Join(root, "Dune (2021)", "Dune.2021.2160p.DDP5.1.mkv"))
	mkvideo(t, filepath.Join(root, "Blade.Runner.2049.2017.1080p.mkv"))
	st, s, lib := newLib(t, "mixed", root)

	eps := scanEpisodes(t, st, s, lib)
	if len(eps) != 2 || eps["Guest Name"] == nil || eps["Title"] == nil {
		t.Fatalf("episodes: %v", eps)
	}
	shows, _ := st.ItemsByKind(lib.ID, "show")
	titles := map[string]bool{}
	for _, sh := range shows {
		titles[sh.Title] = true
	}
	if !titles["The Daily Show"] || !titles["Show"] || len(shows) != 2 {
		t.Errorf("shows: %v", titles)
	}
	movies, _ := st.ItemsByKind(lib.ID, "movie")
	if len(movies) != 2 {
		t.Errorf("got %d movies, want 2", len(movies))
	}
}
