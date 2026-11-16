package meta

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

// Date-named episodes are placed in the season and number the provider
// gives the episode that aired that day, then filled like any other.
func TestRefreshPlacesDatedEpisodes(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, _ := st.CreateLibrary("TV", "shows", []string{"/tv"})
	showID, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "show", Title: "Daily", Path: "/tv/Daily"})
	show, _ := st.Item(showID)
	show.ProviderIDs = map[string]string{"tvmaze": "1", "source": "tvmaze"}
	show.MetaStatus = store.MetaMatched
	st.SaveMetadata(show)
	year, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "season", Title: "Season 2024", ParentID: showID, ShowID: showID, Season: 2024, Path: store.SeasonKey(show.Path, 2024)})
	s29, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "season", Title: "Season 29", ParentID: showID, ShowID: showID, Season: 29, Path: store.SeasonKey(show.Path, 29)})
	dated := func(date, title string, ep int) int64 {
		id, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "episode", Title: title, ParentID: year, ShowID: showID, Season: 2024, Episode: ep, Premiere: date, Path: show.Path + "|D" + date})
		return id
	}
	placed := dated("2024-03-05", "Guest Name", 305)
	taken := dated("2024-03-06", "Other", 306)     // its place is already a file
	missing := dated("2024-03-07", "Unaired", 307) // the provider doesn't list it
	numbered, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "episode", Title: "Episode 46", ParentID: s29, ShowID: showID, Season: 29, Episode: 46, Path: show.Path + "|S29E46"})

	num := func(n int) *int { return &n }
	a := NewAgent(st, NewImageCache(filepath.Join(t.TempDir(), "images")), logx.New(50, false))
	a.tvmaze.cache["1"] = []tvmazeEpisode{
		{Season: 29, Number: num(44), Name: "Before", Airdate: "2024-03-04"},
		{Season: 29, Number: num(45), Name: "Provider Guest", Airdate: "2024-03-05"},
		{Season: 29, Number: num(46), Name: "Provider Other", Airdate: "2024-03-06"},
		{Season: 0, Name: "A special", Airdate: "2024-03-07", Type: "significant_special"},
		// A season numbered like a scanner's MMDD must not be read as one.
		{Season: 2024, Number: num(307), Name: "Wrong", Airdate: "2025-01-01"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // offline: everything comes from the cached episode list
	if err := a.refreshChildren(ctx, show, false, nil); err != nil {
		t.Fatal(err)
	}

	it, _ := st.Item(placed)
	if it.Season != 29 || it.Episode != 45 || it.Title != "Provider Guest" || it.MetaStatus != store.MetaMatched || it.ParentID != s29 {
		t.Errorf("placed: S%dE%d %q status %d parent %d", it.Season, it.Episode, it.Title, it.MetaStatus, it.ParentID)
	}
	it, _ = st.Item(taken)
	if it.Season != 2024 || it.Episode != 306 || it.ParentID != year {
		t.Errorf("taken: moved to S%dE%d", it.Season, it.Episode)
	}
	it, _ = st.Item(missing)
	if it.Season != 2024 || it.Title != "Unaired" || it.Premiere != "2024-03-07" || it.MetaStatus != store.MetaNotFound {
		t.Errorf("missing: S%dE%d %q %q status %d", it.Season, it.Episode, it.Title, it.Premiere, it.MetaStatus)
	}
	if it, _ := st.Item(numbered); it.Title != "Provider Other" {
		t.Errorf("numbered episode: %q", it.Title)
	}
}

func TestTMDBSeasonsFor(t *testing.T) {
	seasons := []tmdbSeason{{0, "2000-01-01"}, {2, "2023-09-25"}, {1, "2022-09-26"}, {3, "2024-09-23"}, {4, ""}}
	cases := map[string][]int{
		"2022-01-01": {1},
		"2022-09-26": {1, 2},
		"2024-03-05": {2, 3},
		"2025-06-01": {3},
	}
	for date, want := range cases {
		if got := tmdbSeasonsFor(seasons, date); !slices.Equal(got, want) {
			t.Errorf("%s: %v want %v", date, got, want)
		}
	}
}
