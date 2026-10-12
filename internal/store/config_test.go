package store

import (
	"encoding/json"
	"slices"
	"testing"
)

// Saved Live TV channels each get a unique id, a name and a known kind, and
// empty lists rather than null.
func TestNormalizeTVChannels(t *testing.T) {
	c := DefaultConfig()
	c.TVChannels = []TVChannel{
		{ID: "a", Name: "  Movies  ", Kind: "movie", Libraries: []int64{1, 0}, Exclude: []int64{3, 3, -1}},
		{ID: "a", Kind: "film", Genres: []string{"Crime", " "}},
		{Kind: "picked", Titles: []int64{2, 1, 2}},
		{},
	}
	c.normalize()
	chs := c.TVChannels
	if len(chs) != 4 || chs[0].ID != "a" || chs[1].ID == "a" || chs[3].ID == "" || chs[1].ID == chs[3].ID {
		t.Fatalf("ids = %+v", chs)
	}
	if chs[0].Name != "Movies" || chs[1].Name != "Channel 2" || chs[0].Kind != "movie" || chs[1].Kind != "" {
		t.Fatalf("names and kinds = %+v", chs)
	}
	if len(chs[0].Libraries) != 1 || len(chs[1].Genres) != 1 || !slices.Equal(chs[0].Exclude, []int64{3}) || chs[2].Kind != "picked" || !slices.Equal(chs[2].Titles, []int64{2, 1}) {
		t.Fatalf("filters = %+v", chs)
	}
	b, _ := json.Marshal(chs[3])
	if want := `{"id":"` + chs[3].ID + `","name":"Channel 4","libraries":[],"genres":[],"kind":"","titles":[],"exclude":[]}`; string(b) != want {
		t.Fatalf("empty channel = %s, want %s", b, want)
	}
	d := DefaultConfig()
	d.normalize()
	if d.TVChannels == nil || d.TVHidden == nil {
		t.Fatal("no channels encode as null")
	}
	d.TVHidden = []string{"lib-1", " ", "lib-1", " genre-Drama "}
	d.normalize()
	if !slices.Equal(d.TVHidden, []string{"lib-1", "genre-Drama"}) {
		t.Fatalf("hidden = %q", d.TVHidden)
	}
}
