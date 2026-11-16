package api

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lex/internal/logx"
	"lex/internal/meta"
	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/trickplay"
)

// cleanupFixture is a server with a real data folder layout, one live file
// (with an item) and helpers to plant derived files.
type cleanupFixture struct {
	s          *Server
	data       string
	media      string
	file       *store.File
	itemID     int64
	downloaded string // a recorded downloaded subtitle of file
}

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	s, _ := securityServer(t)
	data := t.TempDir()
	s.DataDir = data
	log := logx.New(10, false)
	s.Subs = stream.NewSubs(filepath.Join(data, "subs"), "ffmpeg", "ffprobe", log)
	s.Trick = trickplay.New(s.St, log, "ffmpeg", filepath.Join(data, "trickplay"))
	s.Images = meta.NewImageCache(filepath.Join(data, "images"))
	media := t.TempDir()
	lib, err := s.St.CreateLibrary("Movies", "movies", []string{media})
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Movie"})
	if err != nil {
		t.Fatal(err)
	}
	f := &store.File{ItemID: itemID, LibraryID: lib.ID, Path: filepath.Join(media, "Movie.mkv"), Size: 5, Mtime: 100}
	if err := os.WriteFile(f.Path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.St.InsertFile(f); err != nil {
		t.Fatal(err)
	}
	fx := &cleanupFixture{s: s, data: data, media: media, file: f, itemID: itemID}
	fx.downloaded = fx.plant(t, fmt.Sprintf("subs/downloaded/%d-7.srt", f.ID), false)
	if _, err := s.St.AddDownloadedSub(&store.DownloadedSub{FileID: f.ID, Language: "eng", Path: fx.downloaded}); err != nil {
		t.Fatal(err)
	}
	return fx
}

// plant creates a file (or a folder holding one) under the data folder,
// an hour old so the sweep's grace period doesn't protect it.
func (fx *cleanupFixture) plant(t *testing.T, rel string, dir bool) string {
	t.Helper()
	p := filepath.Join(fx.data, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if dir {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(p, "1.jpg"), []byte("x"), 0o644)
	} else if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(p, old, old)
	return p
}

func present(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	return err == nil
}

func TestSweepDerived(t *testing.T) {
	fx := newCleanupFixture(t)
	id := fx.file.ID
	keep := []string{
		fx.downloaded,
		fx.plant(t, fmt.Sprintf("subs/%d-100-2.vtt", id), false),
		fx.plant(t, fmt.Sprintf("subs/%d-100-2.ass", id), false),
		fx.plant(t, fmt.Sprintf("subs/%d-100-1005-0123456789ab.vtt", id), false),
		fx.plant(t, fmt.Sprintf("subs/%d-100-%d.vtt", id, store.DownloadedSubBase+7), false),
		fx.plant(t, fmt.Sprintf("subs/.%d-100-1005-0123456789ab.vtt.42.tmp", id), false), // conversion in progress
		fx.plant(t, fmt.Sprintf("subs/fonts/%d-100", id), true),
		fx.plant(t, fmt.Sprintf("trickplay/%d-100", id), true),
		fx.plant(t, fmt.Sprintf("trickplay/%d-100.tmp", id), true), // generation in progress
		fx.plant(t, fmt.Sprintf("images/thumb-%d.jpg", fx.itemID), false),
		// Not ours to judge: unknown names are never touched.
		fx.plant(t, "subs/notes.txt", false),
		fx.plant(t, "subs/readme.vtt", false),
		fx.plant(t, "subs/downloaded/mine.srt", false),
		fx.plant(t, "trickplay/keep-me", true),
		fx.plant(t, "trickplay/999-1.txt", false), // trickplay entries are folders
		fx.plant(t, "images/0123456789abcdef0123.jpg", false),
		fx.plant(t, "images/r/320-0123456789abcdef0123.jpg", false),
		fx.plant(t, "images/thumb-x.jpg", false),
		fx.plant(t, "lex.db", false),
	}
	drop := []string{
		// Older version of the live file.
		fx.plant(t, fmt.Sprintf("subs/%d-99-2.vtt", id), false),
		fx.plant(t, fmt.Sprintf("subs/fonts/%d-99", id), true),
		fx.plant(t, fmt.Sprintf("trickplay/%d-99", id), true),
		// Sidecar conversion under the old positional key.
		fx.plant(t, fmt.Sprintf("subs/%d-100-1003.vtt", id), false),
		// A file that's gone.
		fx.plant(t, "subs/999-5-2.vtt", false),
		fx.plant(t, "subs/999-5-2.ass", false),
		fx.plant(t, "subs/999-5-2.vtt.tmp", false),
		fx.plant(t, "subs/fonts/999-5", true),
		fx.plant(t, "subs/downloaded/999-3.srt", false),
		fx.plant(t, "trickplay/999-5", true),
		fx.plant(t, "trickplay/999--5.tmp", true),
		fx.plant(t, "images/thumb-12345.jpg", false),
		fx.plant(t, "images/thumb-12345.jpg.tmp.jpg", false),
	}
	// Orphans made moments ago may belong to a file the sweep hasn't seen.
	recent := []string{
		filepath.Join(fx.data, "subs", "998-1-2.vtt"),
		filepath.Join(fx.data, "trickplay", "998-1"),
	}
	os.WriteFile(recent[0], []byte("x"), 0o644)
	os.MkdirAll(recent[1], 0o755)
	keep = append(keep, recent...)

	// A symlink named like an orphan, pointing at the media folder: neither
	// the link's target nor the media file may be touched.
	link := filepath.Join(fx.data, "trickplay", "997-1")
	if err := os.Symlink(fx.media, link); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, link, fx.file.Path)

	fx.s.SweepDerived()
	for _, p := range keep {
		if !present(t, p) {
			t.Errorf("sweep removed %s", strings.TrimPrefix(p, fx.data))
		}
	}
	for _, p := range drop {
		if present(t, p) {
			t.Errorf("sweep kept %s", strings.TrimPrefix(p, fx.data))
		}
	}
	if b, err := os.ReadFile(fx.file.Path); err != nil || string(b) != "video" {
		t.Fatalf("media file changed: %q %v", b, err)
	}
}

func TestSweepDerivedAfterFileRemoved(t *testing.T) {
	fx := newCleanupFixture(t)
	id := fx.file.ID
	files := []string{
		fx.plant(t, fmt.Sprintf("subs/%d-100-2.vtt", id), false),
		fx.plant(t, fmt.Sprintf("trickplay/%d-100", id), true),
		fx.plant(t, fmt.Sprintf("images/thumb-%d.jpg", fx.itemID), false),
	}
	// Removed while Lex was down: the row (and its downloaded_subs row) go,
	// the item is pruned.
	if err := fx.s.St.DeleteFile(id); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.St.DB().Exec(`DELETE FROM items WHERE id=?`, fx.itemID); err != nil {
		t.Fatal(err)
	}
	fx.s.SweepDerived()
	for _, p := range append(files, fx.downloaded) {
		if present(t, p) {
			t.Errorf("kept %s", strings.TrimPrefix(p, fx.data))
		}
	}
}

func TestFileChangedDropsDerived(t *testing.T) {
	fx := newCleanupFixture(t)
	id := fx.file.ID
	other := fx.plant(t, fmt.Sprintf("subs/%d-100-2.vtt", id+1), false)
	prefix := fx.plant(t, fmt.Sprintf("subs/%d1-100-2.vtt", id), false) // id 11 when id is 1
	gone := []string{
		fx.plant(t, fmt.Sprintf("subs/%d-100-2.vtt", id), false),
		fx.plant(t, fmt.Sprintf("subs/%d-100-1005-0123456789ab.vtt", id), false),
		fx.plant(t, fmt.Sprintf("subs/fonts/%d-100", id), true),
		fx.plant(t, fmt.Sprintf("trickplay/%d-100", id), true),
		fx.plant(t, fmt.Sprintf("images/thumb-%d.jpg", fx.itemID), false),
	}
	fx.s.FileChanged(fx.file)
	for _, p := range gone {
		if present(t, p) {
			t.Errorf("kept %s", strings.TrimPrefix(p, fx.data))
		}
	}
	for _, p := range []string{other, prefix, fx.downloaded, fx.file.Path} {
		if !present(t, p) {
			t.Errorf("removed %s", strings.TrimPrefix(p, fx.data))
		}
	}
}

func TestSubsClearRemovesFontsKeepsDownloads(t *testing.T) {
	fx := newCleanupFixture(t)
	fx.plant(t, "subs/1-100-2.vtt", false)
	fx.plant(t, "subs/fonts/1-100", true)
	if err := fx.s.Subs.Clear(); err != nil {
		t.Fatal(err)
	}
	var left []string
	ents, _ := os.ReadDir(filepath.Join(fx.data, "subs"))
	for _, e := range ents {
		left = append(left, e.Name())
	}
	if !slices.Equal(left, []string{"downloaded"}) || !present(t, fx.downloaded) {
		t.Fatalf("left %v (downloaded present: %v)", left, present(t, fx.downloaded))
	}
}

func TestMarkFileChangedBumpsItem(t *testing.T) {
	fx := newCleanupFixture(t)
	before, _ := fx.s.St.Item(fx.itemID)
	fx.s.St.DB().Exec(`UPDATE items SET updated_at=1 WHERE id=?`, fx.itemID)
	if err := fx.s.St.MarkFileChanged(fx.file.ID, 6, 200); err != nil {
		t.Fatal(err)
	}
	after, _ := fx.s.St.Item(fx.itemID)
	if after.UpdatedAt <= 1 || before == nil {
		t.Fatalf("updated_at = %d, want bumped so artwork URLs change", after.UpdatedAt)
	}
}
