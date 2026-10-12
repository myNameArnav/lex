package store

import (
	"encoding/json"
	"testing"
)

// Saved Live TV channels each get a unique id, a name and a known kind, and
// empty lists rather than null.
func TestNormalizeTVChannels(t *testing.T) {
	c := DefaultConfig()
	c.TVChannels = []TVChannel{
		{ID: "a", Name: "  Movies  ", Kind: "movie", Libraries: []int64{1, 0}},
		{ID: "a", Kind: "film", Genres: []string{"Crime", " "}},
		{},
	}
	c.normalize()
	chs := c.TVChannels
	if len(chs) != 3 || chs[0].ID != "a" || chs[1].ID == "a" || chs[2].ID == "" || chs[1].ID == chs[2].ID {
		t.Fatalf("ids = %+v", chs)
	}
	if chs[0].Name != "Movies" || chs[1].Name != "Channel 2" || chs[0].Kind != "movie" || chs[1].Kind != "" {
		t.Fatalf("names and kinds = %+v", chs)
	}
	if len(chs[0].Libraries) != 1 || len(chs[1].Genres) != 1 {
		t.Fatalf("filters = %+v", chs)
	}
	b, _ := json.Marshal(chs[2])
	if want := `{"id":"` + chs[2].ID + `","name":"Channel 3","libraries":[],"genres":[],"kind":""}`; string(b) != want {
		t.Fatalf("empty channel = %s, want %s", b, want)
	}
	d := DefaultConfig()
	d.normalize()
	if d.TVChannels == nil {
		t.Fatal("no channels encode as null")
	}
}
