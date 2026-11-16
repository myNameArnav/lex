package library

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
