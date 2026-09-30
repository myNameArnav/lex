package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

func testCache(t *testing.T, minFreeGB int) (*Cache, *store.Store, int64) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := st.Config()
	cfg.CacheEnabled, cfg.CacheDir, cfg.CacheMaxGB, cfg.CacheMinFreeGB, cfg.CacheSpeedMBs = true, t.TempDir(), 1, minFreeGB, 0
	if _, err := st.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	lib, err := st.CreateLibrary("Movies", "movies", []string{"/m"})
	if err != nil {
		t.Fatal(err)
	}
	item, err := st.InsertItem(&store.Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Path: "/m/Heat"})
	if err != nil {
		t.Fatal(err)
	}
	return New(st, logx.New(50, false), t.TempDir()), st, item
}

func addFile(t *testing.T, st *store.Store, item int64, path string, size int64) *store.File {
	t.Helper()
	f := &store.File{ItemID: item, LibraryID: 1, Path: path, Size: size, Mtime: 1}
	if _, err := st.InsertFile(f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRemovedFileLeavesNoCopy(t *testing.T) {
	c, st, item := testCache(t, 0)
	src := filepath.Join(t.TempDir(), "Heat.mkv")
	os.WriteFile(src, make([]byte, 4096), 0o644)
	f := addFile(t, st, item, src, 4096)
	c.Request(f, "playing")
	deadline := time.Now().Add(5 * time.Second)
	for !c.IsCached(f.ID) {
		if time.Now().After(deadline) {
			t.Fatalf("not cached: %s", c.Status().LastError)
		}
		time.Sleep(10 * time.Millisecond)
	}
	copyPath := c.Peek(f)
	if copyPath == src {
		t.Fatal("Peek ignores the cached copy")
	}
	// The same id with different contents never reads the old copy.
	changed := *f
	changed.Mtime++
	if p := c.Peek(&changed); p != src {
		t.Fatalf("changed file resolved to %s", p)
	}

	// The scanner deletes the file (cache_entries cascade) and tells us.
	st.DeleteFile(f.ID)
	c.Remove(f.ID)
	if c.IsCached(f.ID) {
		t.Fatal("deleted file still reported cached")
	}
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("copy of a deleted file left on disk: %v", err)
	}
}

func TestMakeRoomKeepsRecentlyPlayed(t *testing.T) {
	c, st, item := testCache(t, 0)
	playing := addFile(t, st, item, "/m/a.mkv", 600<<20)
	old := addFile(t, st, item, "/m/b.mkv", 300<<20)
	now := time.Now().Unix()
	st.DB().Exec(`INSERT INTO cache_entries(file_id,path,size,src_mtime,added_at,last_access) VALUES(?,?,?,?,?,?),(?,?,?,?,?,?)`,
		playing.ID, "/x/a", playing.Size, 1, now-7200, now-3*3600,
		old.ID, "/x/b", old.Size, 1, now-86400, now-86400)
	c.disk = func(string) (int64, int64, bool) { return 100 << 30, 200 << 30, true }

	if err := c.makeRoom(300 << 20); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	rows, _ := st.DB().Query(`SELECT file_id FROM cache_entries`)
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 || ids[0] != playing.ID {
		t.Fatalf("entries left = %v, want only the one played 3h ago", ids)
	}
	// No room without evicting what's being watched: give up, keep it.
	if err := c.makeRoom(500 << 20); err == nil {
		t.Fatal("evicted a recently played file or overfilled the cache")
	}
	if c.used() != playing.Size {
		t.Fatal("recently played entry evicted")
	}
}

func TestMakeRoomDoesNotEmptyCacheInVain(t *testing.T) {
	c, st, item := testCache(t, 20)
	a := addFile(t, st, item, "/m/a.mkv", 100<<20)
	st.DB().Exec(`INSERT INTO cache_entries(file_id,path,size,src_mtime,added_at,last_access) VALUES(?,?,?,?,?,?)`,
		a.ID, "/x/a", a.Size, 1, 0, 0)
	// 1 GB free, 20 GB to keep free: evicting 100 MB can't help.
	c.disk = func(string) (int64, int64, bool) { return 1 << 30, 200 << 30, true }
	if err := c.makeRoom(10 << 20); err == nil {
		t.Fatal("want an error")
	}
	if c.used() != a.Size {
		t.Fatal("entry evicted although that couldn't make room")
	}
}
