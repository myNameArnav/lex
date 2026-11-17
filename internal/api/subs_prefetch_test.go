package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lex/internal/cache"
	"lex/internal/store"
	"lex/internal/stream"
)

// While the played file is copied to the SSD cache, its subtitle prefetch
// (a whole-file read) waits, and runs once the copy is done.
func TestSubsPrefetchWaitsForCacheCopy(t *testing.T) {
	s, _ := securityServer(t)
	cfg := s.St.Config()
	cfg.CacheEnabled, cfg.CacheDir, cfg.CacheMaxGB, cfg.CacheSpeedMBs, cfg.CacheMinFreeGB = true, t.TempDir(), 1, 1, 0
	s.St.SaveConfig(cfg)
	s.Cache = cache.New(s.St, s.Log, t.TempDir())
	s.Cache.OnCached = s.FileCached
	s.Subs = stream.NewSubs(t.TempDir(), "ffmpeg", "ffprobe", s.Log)
	src := filepath.Join(t.TempDir(), "Heat.mkv")
	os.WriteFile(src, make([]byte, 2<<20), 0o644)
	lib, _ := s.St.CreateLibrary("Movies", "movies", []string{filepath.Dir(src)})
	item, _ := s.St.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Path: src})
	f := &store.File{ItemID: item, LibraryID: lib.ID, Path: src, Size: 2 << 20, Mtime: 1}
	if _, err := s.St.InsertFile(f); err != nil {
		t.Fatal(err)
	}
	waiting := func() bool {
		s.subsAfterCacheMu.Lock()
		defer s.subsAfterCacheMu.Unlock()
		return s.subsAfterCache[f.ID]
	}

	s.Cache.Request(f, "playing")
	s.prefetchSubs(f)
	if !waiting() {
		t.Fatal("prefetch didn't wait for the cache copy")
	}
	deadline := time.Now().Add(10 * time.Second)
	for waiting() {
		if time.Now().After(deadline) {
			t.Fatal("prefetch still waiting after the copy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Cache.IsCached(f.ID) {
		t.Fatal("not cached")
	}
	// Cached already: nothing to wait for.
	s.prefetchSubs(f)
	if waiting() {
		t.Fatal("waited for a file that's already cached")
	}
}
