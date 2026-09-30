package store

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"testing"
)

// movieFixture makes a library with two movies, the first one watched and
// manually matched, and a file on it.
func movieFixture(t *testing.T) (st *Store, uid, oldItem, newItem, fileID int64) {
	t.Helper()
	st = testStore(t)
	lib, err := st.CreateLibrary("Movies", "movies", []string{"/m"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("viewer", "correct horse battery staple", false)
	if err != nil {
		t.Fatal(err)
	}
	oldItem, _ = st.InsertItem(&Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Year: 1995, Path: "/m/Heat"})
	newItem, _ = st.InsertItem(&Item{LibraryID: lib.ID, Kind: "movie", Title: "Heat", Year: 1995, Path: "/m/Heat#heat1995"})
	fileID, err = st.InsertFile(&File{ItemID: oldItem, LibraryID: lib.ID, Path: "/m/Heat/Heat.mkv", Size: 10, Mtime: 1})
	if err != nil {
		t.Fatal(err)
	}
	it, _ := st.Item(oldItem)
	it.ProviderIDs = map[string]string{"tmdb": "949", "source": "tmdb"}
	it.MetaLocked, it.MetaStatus, it.Title = true, MetaMatched, "Heat (Director's pick)"
	if err := st.SaveMetadata(it); err != nil {
		t.Fatal(err)
	}
	st.SaveProgress(u.ID, oldItem, 600)
	return st, u.ID, oldItem, newItem, fileID
}

func TestRelocateFileCarriesManualMatch(t *testing.T) {
	st, uid, oldItem, newItem, fileID := movieFixture(t)
	st.DB().Exec(`INSERT INTO history(user_id,item_id,file_id,started_at,ended_at) VALUES(?,?,?,1,2)`, uid, oldItem, fileID)
	if err := st.RelocateFile(fileID, "/m/Heat/Heat.1995.mkv", oldItem, newItem); err != nil {
		t.Fatal(err)
	}
	it, _ := st.Item(newItem)
	if !it.MetaLocked || it.ProviderIDs["tmdb"] != "949" || it.MetaStatus != MetaMatched || it.Title != "Heat (Director's pick)" {
		t.Fatalf("manual match not carried: %+v", it)
	}
	if ud, _ := st.GetUserData(uid, newItem); ud.Position != 600 {
		t.Fatalf("progress not carried: %+v", ud)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM history WHERE item_id=?`, newItem).Scan(&n)
	if n != 1 {
		t.Fatalf("history not moved")
	}
	f, _ := st.File(fileID)
	if f.Path != "/m/Heat/Heat.1995.mkv" || f.ItemID != newItem {
		t.Fatalf("file = %+v", f)
	}
}

func TestMoveFileMergesUserData(t *testing.T) {
	st, uid, oldItem, newItem, fileID := movieFixture(t)
	// The target already has newer progress and a lock of its own.
	st.DB().Exec(`INSERT INTO user_data(user_id,item_id,position,last_played,favorite) VALUES(?,?,?,?,0)`, uid, newItem, 900, now()+10)
	st.SetFavorite(uid, oldItem, true)
	st.DB().Exec(`UPDATE items SET meta_locked=1, provider_ids='{"tmdb":"1"}' WHERE id=?`, newItem)
	if err := st.MoveFile(fileID, oldItem, newItem); err != nil {
		t.Fatal(err)
	}
	ud, _ := st.GetUserData(uid, newItem)
	if ud.Position != 900 || !ud.Favorite {
		t.Fatalf("merged user data = %+v, want newer position and the favorite", ud)
	}
	if it, _ := st.Item(newItem); it.ProviderIDs["tmdb"] != "1" {
		t.Fatalf("target's own lock was overwritten: %v", it.ProviderIDs)
	}
}

func TestFailedProbesAreRetriedWithBackoff(t *testing.T) {
	st, _, _, _, fileID := movieFixture(t)
	needs := func() bool {
		t.Helper()
		files, err := st.FilesNeedingProbe()
		if err != nil {
			t.Fatal(err)
		}
		return len(files) == 1
	}
	if !needs() {
		t.Fatal("new file not queued for probing")
	}
	st.SaveProbe(fileID, nil, "i/o timeout")
	if needs() {
		t.Fatal("failed probe retried immediately")
	}
	age := func(secs int64) { st.DB().Exec(`UPDATE files SET probed_at=? WHERE id=?`, now()-secs, fileID) }
	age(3600)
	if !needs() {
		t.Fatal("failed probe not retried after an hour")
	}
	// Second failure: the next try waits two hours.
	st.SaveProbe(fileID, nil, "i/o timeout")
	age(3600)
	if needs() {
		t.Fatal("second retry came too soon")
	}
	age(2 * 3600)
	if !needs() {
		t.Fatal("second retry never came")
	}
	// Many failures: at most weekly.
	st.DB().Exec(`UPDATE files SET probe_attempts=40 WHERE id=?`, fileID)
	age(6 * 24 * 3600)
	if needs() {
		t.Fatal("corrupt file retried more than weekly")
	}
	age(7 * 24 * 3600)
	if !needs() {
		t.Fatal("corrupt file never retried")
	}
	// Success clears the failure.
	st.SaveProbe(fileID, &MediaInfo{Format: "matroska"}, "")
	age(30 * 24 * 3600)
	if needs() {
		t.Fatal("probed file queued again")
	}
}

func TestSaveMetadataIfLosesToManualMatch(t *testing.T) {
	st, _, oldItem, _, _ := movieFixture(t)
	snap, _ := st.Item(oldItem)
	g := GuardOf(snap)
	// A manual match lands while the automatic refresh is busy.
	manual, _ := st.Item(oldItem)
	manual.ProviderIDs = map[string]string{"tmdb": "1", "source": "tmdb"}
	if err := st.SaveMetadata(manual); err != nil {
		t.Fatal(err)
	}
	snap.ProviderIDs = map[string]string{"tmdb": "2", "source": "tmdb"}
	snap.MetaLocked = false
	if ok, err := st.SaveMetadataIf(snap, g); err != nil || ok {
		t.Fatalf("stale automatic save written: ok=%v err=%v", ok, err)
	}
	it, _ := st.Item(oldItem)
	if it.ProviderIDs["tmdb"] != "1" || !it.MetaLocked {
		t.Fatalf("manual match reverted: %+v", it)
	}
	// With an unchanged match the save goes through.
	if ok, err := st.SaveMetadataIf(it, GuardOf(it)); err != nil || !ok {
		t.Fatalf("fresh automatic save refused: ok=%v err=%v", ok, err)
	}
}

func TestOpenAddsLateColumns(t *testing.T) {
	dir := t.TempDir()
	// A database from before probe_attempts existed.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "lex.db")}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE files (id INTEGER PRIMARY KEY, item_id INTEGER NOT NULL, library_id INTEGER NOT NULL,
		path TEXT NOT NULL UNIQUE, size INTEGER NOT NULL, mtime INTEGER NOT NULL, container TEXT NOT NULL DEFAULT '',
		duration REAL NOT NULL DEFAULT 0, bitrate INTEGER NOT NULL DEFAULT 0, width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0, vcodec TEXT NOT NULL DEFAULT '', acodec TEXT NOT NULL DEFAULT '',
		hdr TEXT NOT NULL DEFAULT '', info TEXT NOT NULL DEFAULT '', probed_at INTEGER NOT NULL DEFAULT 0,
		probe_error TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 { // and again, now that it has the column
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.FilesNeedingProbe(); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
}
