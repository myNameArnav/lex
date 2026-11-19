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
	// The order and the channels turned off forget the admin's channels
	// that are gone, but keep automatic ones the library may fill again.
	d.TVChannels = []TVChannel{{ID: "a", Name: "A"}}
	d.TVOrder = []string{"custom-gone", "genre-Western", "custom-a", "custom-a"}
	d.TVHidden = []string{"custom-gone", "custom-a"}
	d.normalize()
	if !slices.Equal(d.TVOrder, []string{"genre-Western", "custom-a"}) || !slices.Equal(d.TVHidden, []string{"custom-a"}) {
		t.Fatalf("order = %q, hidden = %q", d.TVOrder, d.TVHidden)
	}
}

// Remuxing in the browser is on by default, also for settings saved before
// the option existed, and can be turned off.
func TestClientRemuxSetting(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Config().ClientRemux {
		t.Fatal("off by default")
	}
	if err := s.setKV("config", `{"clientAudio":false}`); err != nil {
		t.Fatal(err)
	}
	if err := s.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if c := s.Config(); !c.ClientRemux || c.ClientAudio {
		t.Fatalf("older settings: clientRemux=%v clientAudio=%v", c.ClientRemux, c.ClientAudio)
	}
	c := s.Config()
	c.ClientRemux = false
	if _, err := s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := s.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if s.Config().ClientRemux {
		t.Fatal("turning it off didn't stick")
	}
}

// History is kept a year by default, also for settings saved before the
// option existed; 0 keeps it forever and out-of-range values are clamped.
func TestHistoryRetentionSetting(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if d := s.Config().HistoryRetentionDays; d != 365 {
		t.Fatalf("default %d", d)
	}
	if err := s.setKV("config", `{"serverName":"Old"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if d := s.Config().HistoryRetentionDays; d != 365 {
		t.Fatalf("older settings: %d", d)
	}
	for _, c := range []struct{ in, want int }{{0, 0}, {30, 30}, {-1, 0}, {100000, 36500}} {
		cfg := s.Config()
		cfg.HistoryRetentionDays = c.in
		if _, err := s.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if err := s.loadConfig(); err != nil {
			t.Fatal(err)
		}
		if d := s.Config().HistoryRetentionDays; d != c.want {
			t.Fatalf("saved %d, got %d, want %d", c.in, d, c.want)
		}
	}
}
