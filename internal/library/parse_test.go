package library

import "testing"

func TestCleanTitle(t *testing.T) {
	cases := []struct {
		in    string
		title string
		year  int
	}{
		{"The Office (US) (2005) Season 1-9 S01-S09 (1080p BluRay x265 HEVC 10bit AAC 5.1 Silence)", "The Office (US)", 2005},
		{"Minions.And.Monsters.2026.2160p.iT.WEB-DL.DV.HDR10+.DDP5.1.Atmos.H265.MP4-BTM", "Minions And Monsters", 2026},
		{"Project Hail Mary (2026) IMAX (1080p AMZN WEB-DL x265 10bit EAC3 Atmos 5.1 Silence)", "Project Hail Mary", 2026},
		{"www.Torrenting.com - Venom Let There Be Carnage 2021 1080p WebRip EAC3 5 1 x265-Lootera", "Venom Let There Be Carnage", 2021},
		{"Dune Part Two 2024 REPACK 1080p AMZN WEB-DL DDP5 1 Atmos H 264-FLUX", "Dune Part Two", 2024},
		{"Dune (2021) [1080p] [WEBRip] [5.1] [YTS.MX]", "Dune", 2021},
		{"Mad Max (1979)", "Mad Max", 1979},
		{"Mad Max - Fury Road (2015)", "Mad Max - Fury Road", 2015},
		{"Everything.Everywhere.All.At.Once.2022.1080p.WEBRip.DDP5.1.x264-NOGRP", "Everything Everywhere All At Once", 2022},
		{"Blade.Runner.2049.2017.1080p.BluRay", "Blade Runner 2049", 2017},
		{"1917.2019.1080p", "1917", 2019},
		{"Demon.Slayer.Kimetsu.no.Yaiba.Infinity.Castle.2025.1080p.CR.WEB-DL.ENG.ITA.JAP.x264-kARiDo", "Demon Slayer Kimetsu no Yaiba Infinity Castle", 2025},
		{"Severance", "Severance", 0},
		{"Chainsaw Man", "Chainsaw Man", 0},
		{"Spider-Man Into the Spider-Verse (2018) Remux-1080p", "Spider-Man Into the Spider-Verse", 2018},
		{"Some.Show.720p.HDTV", "Some Show", 0},
		{"Big.Test.2020.1080p.WEB-DL", "Big Test", 2020},
	}
	for _, c := range cases {
		title, year := CleanTitle(c.in)
		if title != c.title || year != c.year {
			t.Errorf("CleanTitle(%q) = %q,%d want %q,%d", c.in, title, year, c.title, c.year)
		}
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		in       string
		dir      int
		abs      bool
		s, e, ee int
		ok       bool
	}{
		{"The Office (US) - S02E02 - Sexual Harassment Bluray-1080p.mkv", 2, false, 2, 2, 0, true},
		{"The Office (US) - S06E17-E18 - The Delivery Bluray-1080p.mkv", 6, false, 6, 17, 18, true},
		{"Show.S01E01E02.720p.mkv", -1, false, 1, 1, 2, true},
		{"Show.S01E05-06.1080p.mkv", -1, false, 1, 5, 6, true},
		{"Chainsaw Man - S01E12 - KATANA VS. CHAINSAW WEBDL-1080p.mkv", 1, false, 1, 12, 0, true},
		{"show.3x07.hdtv.mkv", -1, false, 3, 7, 0, true},
		{"[SubsPlease] Chainsaw Man - 05 (1080p) [ABC123].mkv", -1, true, 1, 5, 0, true},
		{"Episode 4.mkv", 2, false, 2, 4, 0, true},
		{"03 - Title.mkv", 1, false, 1, 3, 0, true},
		{"Movie.2020.1920x1080.mkv", -1, false, 0, 0, 0, false},
		{"Demon.Slayer.Kimetsu.no.Yaiba.Infinity.Castle.2025.1080p.CR.WEB-DL.ENG.ITA.JAP.x264-kARiDo.mkv", -1, false, 0, 0, 0, false},
	}
	for _, c := range cases {
		ei, ok := ParseEpisode(c.in, c.dir, c.abs)
		if ok != c.ok || (ok && (ei.Season != c.s || ei.Episode != c.e || ei.EpisodeEnd != c.ee)) {
			t.Errorf("ParseEpisode(%q) = %+v,%v want %d,%d,%d,%v", c.in, ei, ok, c.s, c.e, c.ee, c.ok)
		}
	}
}

func TestSeasonFromDir(t *testing.T) {
	cases := map[string]int{"Season 1": 1, "Season 02": 2, "S03": 3, "Specials": 0, "Season 0": 0, "Extras": -1, "The Office (US) (2005) Season 1-9 S01-S09": -1, "Series 4": 4}
	for in, want := range cases {
		if got := SeasonFromDir(in); got != want {
			t.Errorf("SeasonFromDir(%q)=%d want %d", in, got, want)
		}
	}
}

func TestIsSample(t *testing.T) {
	cases := map[string]bool{"Sample Show - S01E01.mkv": false, "movie.sample.mkv": true, "sample-movie.mkv": true, "Movie-sample.mkv": true, "Sampler.mkv": false, "sample.mkv": true}
	for in, want := range cases {
		if got := IsSample(in, 50<<20); got != want {
			t.Errorf("IsSample(%q)=%v want %v", in, got, want)
		}
	}
}

func TestParseEpisodeAirDate(t *testing.T) {
	cases := []struct {
		in   string
		date string // "" = no air date
		s, e int
		ok   bool
	}{
		// Daily-show names.
		{"The.Daily.Show.2024.03.05.Guest.Name.1080p.WEB.mkv", "2024-03-05", 2024, 305, true},
		{"Show - 2024-03-05 - Title.mkv", "2024-03-05", 2024, 305, true},
		{"The Daily Show - 2024-12-31 - Year End HDTV-720p.mkv", "2024-12-31", 2024, 1231, true},
		{"show_2023_01_09_guest.mkv", "2023-01-09", 2023, 109, true},
		{"Show 2023 01 09 Guest.mkv", "2023-01-09", 2023, 109, true},
		{"Tagesschau.05.03.2024.mkv", "2024-03-05", 2024, 305, true},
		{"Show 31-12-2023.mkv", "2023-12-31", 2023, 1231, true},
		{"Show.12.31.2023.mkv", "2023-12-31", 2023, 1231, true}, // MM.DD.YYYY when DD.MM can't be
		{"2024.02.29.Leap.Day.Special.mkv", "2024-02-29", 2024, 229, true},
		{"[Group] Show 2024.03.05 [1080p].mkv", "2024-03-05", 2024, 305, true},
		{"Jeopardy.2024.03.05.Episode.9001.mkv", "2024-03-05", 2024, 305, true},
		// A season marker wins over a date.
		{"Show.S29E45.2024.03.05.mkv", "", 29, 45, true},
		{"Show.3x07.2024.03.05.mkv", "", 3, 7, true},
		// Not dates.
		{"Movie.2020.1080p.WEB.mkv", "", 0, 0, false},
		{"Blade.Runner.2049.2017.1080p.BluRay.mkv", "", 0, 0, false},
		{"2001.A.Space.Odyssey.1968.mkv", "", 0, 0, false},
		{"Movie.2019.7.1.Atmos.mkv", "", 0, 0, false},                  // year + 7.1 audio
		{"Movie.2019.DDP5.1.Atmos.mkv", "", 0, 0, false},               // year + DDP5.1
		{"Movie.2020.1920x1080.mkv", "", 0, 0, false},                  // resolution
		{"Movie.2023.13.45.mkv", "", 0, 0, false},                      // no such month/day
		{"Show.2023.02.30.mkv", "", 0, 0, false},                       // no such day
		{"Show.2024-03.05.mkv", "", 0, 0, false},                       // mixed separators
		{"Movie.1999.10.12bit.mkv", "", 0, 0, false},                   // day runs into "bit"
		{"Movie.x2024.03.05.mkv", "", 0, 0, false},                     // year runs into a letter
		{"Movie.12024.03.05.mkv", "", 0, 0, false},                     // five-digit number
		{"Movie.2024.03.051.mkv", "", 0, 0, false},                     // three-digit day
		{"Old.Film.1912.03.05.mkv", "", 0, 0, false},                   // too early to be a TV air date
		{"Future.2999.03.05.mkv", "", 0, 0, false},                     // not a 19xx/20xx year
		{"Movie.Title.2010.10.10.mkv", "2010-10-10", 2010, 1010, true}, // a real date is a date
	}
	for _, c := range cases {
		ei, ok := ParseEpisode(c.in, -1, false)
		if ok != c.ok || ei.AirDate != c.date || (ok && (ei.Season != c.s || ei.Episode != c.e)) {
			t.Errorf("ParseEpisode(%q) = %+v,%v want date %q %d,%d,%v", c.in, ei, ok, c.date, c.s, c.e, c.ok)
		}
	}
	// A season folder doesn't override the year grouping, and absolute
	// numbering doesn't take the date apart.
	for _, dir := range []int{-1, 2024, 29} {
		ei, ok := ParseEpisode("Show - 2024-03-05 - Title.mkv", dir, true)
		if !ok || ei.AirDate != "2024-03-05" || ei.Season != 2024 || ei.Episode != 305 {
			t.Errorf("dir %d: got %+v,%v", dir, ei, ok)
		}
	}
}
