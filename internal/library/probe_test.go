package library

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lex/internal/store"
)

func TestExternalSubsMatchesOnlyOwnSidecars(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"Alien.mkv", "Aliens.mkv", "Alien.en.srt", "Alien.srt", "Aliens.en.srt", "Alien_forced.fr.srt",
		"Show - 1.mkv", "Show - 1.en.srt", "Show - 10.en.srt", "Show - 12.srt",
		"Film.mkv", "Film Extended.mkv", "Film.en.srt", "Film Extended.en.srt",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		video string
		want  []string
	}{
		{"Alien.mkv", []string{"Alien.en.srt", "Alien.srt", "Alien_forced.fr.srt"}},
		{"Aliens.mkv", []string{"Aliens.en.srt"}},
		{"Show - 1.mkv", []string{"Show - 1.en.srt"}},
		{"Film.mkv", []string{"Film.en.srt"}},
		{"Film Extended.mkv", []string{"Film Extended.en.srt"}},
	} {
		t.Run(tc.video, func(t *testing.T) {
			var got []string
			for _, s := range ExternalSubs(filepath.Join(dir, tc.video)) {
				got = append(got, filepath.Base(s.ExternalPath))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExternalSubIndexesAreStable(t *testing.T) {
	dir := t.TempDir()
	touch := func(n string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	touch("Movie.mkv")
	touch("Movie.fr.srt")
	index := func() map[string]int {
		m := map[string]int{}
		for _, s := range ExternalSubs(filepath.Join(dir, "Movie.mkv")) {
			if s.Index < ExternalSubBase || s.Index >= store.DownloadedSubBase {
				t.Fatalf("index %d out of range", s.Index)
			}
			m[filepath.Base(s.ExternalPath)] = s.Index
		}
		return m
	}
	before := index()["Movie.fr.srt"]
	touch("Movie.en.srt")
	touch("Movie.de.srt")
	after := index()
	if after["Movie.fr.srt"] != before {
		t.Fatalf("adding sidecars moved Movie.fr.srt from %d to %d", before, after["Movie.fr.srt"])
	}
	seen := map[int]bool{}
	for n, i := range after {
		if seen[i] {
			t.Fatalf("duplicate index %d (%s)", i, n)
		}
		seen[i] = true
	}
}

func TestExternalSubIndexCollisions(t *testing.T) {
	used := map[int]bool{}
	a := externalSubIndex("a.srt", used)
	// Fill every other slot; the next name must still get a free, unique one.
	for i := ExternalSubBase; i < store.DownloadedSubBase; i++ {
		if i != ExternalSubBase+500 {
			used[i] = true
		}
	}
	if b := externalSubIndex("b.srt", used); b != ExternalSubBase+500 || b == a {
		t.Fatalf("got %d, want the only free slot", b)
	}
	if c := externalSubIndex("c.srt", used); c != -1 {
		t.Fatalf("got %d with no free slots", c)
	}
}
