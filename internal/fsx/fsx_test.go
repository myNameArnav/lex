package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Version
		ok   bool
	}{
		{"5-100", Version{5, 100}, true},
		{"5-100.tmp", Version{5, 100}, true},
		{"5-100-2.vtt", Version{5, 100}, true},
		{".5-100-2.vtt.123.tmp", Version{5, 100}, true},
		{"5--100", Version{5, -100}, true},
		{"5-100x", Version{}, false},
		{"5-", Version{}, false},
		{"5--", Version{}, false},
		{"-5-100", Version{}, false},
		{"0-100", Version{}, false},
		{"x-100", Version{}, false},
		{"thumb-5.jpg", Version{}, false},
		{"downloaded", Version{}, false},
		{"fonts", Version{}, false},
	} {
		got, ok := ParseVersion(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseVersion(%q) = %v %v, want %v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRemoveRefusesUnsafe(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "movie.mkv")
	os.WriteFile(victim, []byte("v"), 0o644)
	os.Symlink(outside, filepath.Join(dir, "link"))
	os.Symlink(victim, filepath.Join(dir, "filelink"))
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	os.Symlink(outside, filepath.Join(sub, "inner")) // inside a folder that is removed
	for _, c := range []struct{ dir, name string }{
		{dir, "link"}, {dir, "filelink"}, {dir, ".."}, {dir, "."}, {dir, ""}, {dir, "../x"},
		{dir, "sub/inner"}, {"relative", "x"}, {"/", "tmp"},
	} {
		if err := Remove(c.dir, c.name); err == nil {
			t.Errorf("Remove(%q, %q) succeeded", c.dir, c.name)
		}
	}
	if err := Remove(dir, "sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("sub not removed")
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "v" {
		t.Fatal("file outside the folder was touched")
	}
	if err := Remove(dir, "missing"); err != nil {
		t.Fatalf("missing entry: %v", err)
	}
}
