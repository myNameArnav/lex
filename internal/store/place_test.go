package store

import (
	"errors"
	"testing"
)

// PlaceEpisode moves a date-named episode out of its year into the
// provider's season, making that season and dropping the emptied year.
func TestPlaceEpisode(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, _ := st.CreateLibrary("TV", "shows", []string{"/tv"})
	showID, _ := st.InsertItem(&Item{LibraryID: lib.ID, Kind: "show", Title: "Daily", Path: "/tv/Daily"})
	year, _ := st.InsertItem(&Item{LibraryID: lib.ID, Kind: "season", Title: "Season 2024", ParentID: showID, ShowID: showID, Season: 2024, Path: SeasonKey("/tv/Daily", 2024)})
	ep := func(day int) int64 {
		date := "2024-03-0" + string(rune('0'+day))
		id, err := st.InsertItem(&Item{LibraryID: lib.ID, Kind: "episode", Title: date, ParentID: year, ShowID: showID, Season: 2024, Episode: 300 + day, Premiere: date, Path: "/tv/Daily|D" + date})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := ep(5), ep(6)
	if it, _ := st.Item(a); it.Premiere != "2024-03-05" {
		t.Fatalf("premiere not stored on insert: %q", it.Premiere)
	}

	if ok, err := st.PlaceEpisode(a, 29, 45); !ok || err != nil {
		t.Fatalf("place a: %v %v", ok, err)
	}
	it, _ := st.Item(a)
	season, err := st.FindSeason(showID, 29)
	if err != nil {
		t.Fatal(err)
	}
	if it.ParentID != season.ID || it.Season != 29 || it.Episode != 45 {
		t.Fatalf("placed episode: parent %d S%dE%d, season %d", it.ParentID, it.Season, it.Episode, season.ID)
	}
	if season.Path != SeasonKey("/tv/Daily", 29) || season.Title != "Season 29" || season.MetaStatus != MetaPending || season.LibraryID != lib.ID {
		t.Errorf("new season: %+v", season)
	}
	if _, err := st.Item(year); err != nil {
		t.Errorf("year season with an episode left was removed: %v", err)
	}

	// The place is taken: nothing changes.
	if ok, err := st.PlaceEpisode(b, 29, 45); ok || err != nil {
		t.Fatalf("place b on a: %v %v", ok, err)
	}
	if it, _ := st.Item(b); it.Season != 2024 || it.ParentID != year {
		t.Fatalf("b moved onto a taken place: %+v", it)
	}

	// The last episode leaving the year removes it.
	if ok, err := st.PlaceEpisode(b, 29, 46); !ok || err != nil {
		t.Fatalf("place b: %v %v", ok, err)
	}
	if _, err := st.Item(year); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty year season kept: %v", err)
	}
	if ok, err := st.PlaceEpisode(999999, 1, 1); ok || !errors.Is(err, ErrNotFound) {
		t.Errorf("missing episode: %v %v", ok, err)
	}
}
