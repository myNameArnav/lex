package library

import "testing"

func TestEditionAndPart(t *testing.T) {
	for _, c := range []struct {
		path, edition string
		part          int
	}{
		{"/m/Blade Runner (1982)/Blade Runner (1982) Final Cut 1080p.mkv", "Final Cut", 0},
		{"/m/Blade Runner (1982)/Blade Runner (1982) Theatrical 1080p.mkv", "Theatrical", 0},
		{"Aliens.1986.Directors.Cut.1080p.mkv", "Director's Cut", 0},
		{"Aliens (1986) Director's Cut.mkv", "Director's Cut", 0},
		{"The Hobbit (2012) {edition-Extended Edition}.mkv", "Extended Edition", 0},
		{"LOTR.2001.EXTENDED.2160p.mkv", "Extended", 0},
		{"Dune (2021) IMAX.mkv", "IMAX", 0},
		{"Movie (1962) cd1.mkv", "", 1},
		{"Movie (1962) CD2.avi", "", 2},
		{"Movie.1962.part.2.mkv", "", 2},
		{"Movie (1962) - Disc 1.mkv", "", 1},
		{"Movie (1962) - pt3.mkv", "", 3},
		{"Dune Part Two (2024).mkv", "", 0},
		{"Kill Bill Vol 1 (2003).mkv", "", 0},
		{"Toy Story 2 (1999).mkv", "", 0},
		{"Party (2010).mkv", "", 0},
		{"Uncut Gems (2019).mkv", "", 0},
		{"Kill Bill Part 2 (2004).mkv", "", 0},
		{"Kill Bill Part 2 (2004) cd2.mkv", "", 2},
		{"Blade Runner 2049 (2017) IMAX.mkv", "IMAX", 0},
		{"Extended Family.mkv", "Extended", 0}, // no year: the whole name counts
	} {
		if got := Edition(c.path); got != c.edition {
			t.Errorf("Edition(%q) = %q, want %q", c.path, got, c.edition)
		}
		if got := Part(c.path); got != c.part {
			t.Errorf("Part(%q) = %d, want %d", c.path, got, c.part)
		}
	}
}
