package meta

import (
	"context"
	"path/filepath"
	"testing"

	"lex/internal/logx"
	"lex/internal/store"
)

// A season that can't be fetched (network down) stays pending so the next
// run retries it, instead of being recorded as not found for good.
func TestSeasonFetchErrorKeepsEpisodesPending(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	lib, err := st.CreateLibrary("TV", "shows", []string{"/tv"})
	if err != nil {
		t.Fatal(err)
	}
	showID, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "show", Title: "Silo", Path: "/tv/Silo"})
	show, _ := st.Item(showID)
	show.ProviderIDs = map[string]string{"tvmaze": "1", "source": "tvmaze"}
	show.MetaStatus = store.MetaMatched
	st.SaveMetadata(show)
	seasonID, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "season", Title: "Season 1", ParentID: showID, ShowID: showID, Season: 1})
	epID, _ := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "episode", Title: "Episode 1", ParentID: seasonID, ShowID: showID, Season: 1, Episode: 1})

	a := NewAgent(st, NewImageCache(filepath.Join(t.TempDir(), "images")), logx.New(50, false))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every request fails, like an unreachable provider
	if err := a.refreshChildren(ctx, show, true, nil); err == nil {
		t.Fatal("want the fetch error reported")
	}
	for _, id := range []int64{seasonID, epID} {
		it, _ := st.Item(id)
		if it.MetaStatus != store.MetaPending {
			t.Fatalf("%s: status %d after a network error, want pending", it.Kind, it.MetaStatus)
		}
	}
}
