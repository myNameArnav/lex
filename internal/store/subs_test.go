package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func subsFixture(t *testing.T) *Store {
	t.Helper()
	st := testStore(t)
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`,
		`INSERT INTO items(id,library_id,kind,title,sort_title,added_at,updated_at) VALUES(1,1,'movie','Dune','dune',0,0)`,
		`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at) VALUES(1,1,1,'/m/dune.mkv',1,1,0),(2,1,1,'/m/dune2.mkv',1,1,0)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestDownloadedSubReusesSameDownload(t *testing.T) {
	st := subsFixture(t)
	a := &DownloadedSub{FileID: 1, Path: "/d/1-99.srt", Language: "eng", UserID: 5}
	if created, err := st.AddDownloadedSub(a); err != nil || !created {
		t.Fatalf("first add: created=%v err=%v", created, err)
	}
	b := &DownloadedSub{FileID: 1, Path: "/d/1-99.srt", Language: "eng", UserID: 6}
	if created, err := st.AddDownloadedSub(b); err != nil || created {
		t.Fatalf("second add: created=%v err=%v", created, err)
	}
	if b.ID != a.ID || b.UserID != 5 {
		t.Fatalf("second add = %+v, want the first row %+v", b, a)
	}
	// The same subtitle for another file is a separate row.
	c := &DownloadedSub{FileID: 2, Path: "/d/2-99.srt"}
	if created, _ := st.AddDownloadedSub(c); !created || c.ID == a.ID {
		t.Fatalf("other file: %+v", c)
	}
	subs, _ := st.DownloadedSubs(1)
	if len(subs) != 1 || subs[0].UserID != 5 {
		t.Fatalf("rows = %+v", subs)
	}
}

func TestDownloadedSubIDsAreNotReused(t *testing.T) {
	st := subsFixture(t)
	a := &DownloadedSub{FileID: 1, Path: "/d/a.srt"}
	b := &DownloadedSub{FileID: 1, Path: "/d/b.srt"}
	st.AddDownloadedSub(a)
	st.AddDownloadedSub(b)
	if _, err := st.DeleteDownloadedSub(b.ID); err != nil {
		t.Fatal(err)
	}
	c := &DownloadedSub{FileID: 1, Path: "/d/c.srt"}
	st.AddDownloadedSub(c)
	if c.ID <= b.ID {
		t.Fatalf("new id %d reuses deleted id %d", c.ID, b.ID)
	}
}

func TestDeleteDownloadedSubSharedPath(t *testing.T) {
	st := subsFixture(t)
	// Older versions recorded a repeated download as a second row.
	for range 2 {
		if _, err := st.DB().Exec(`INSERT INTO downloaded_subs(file_id,path,created_at) VALUES(1,'/d/1-5.srt',0)`); err != nil {
			t.Fatal(err)
		}
	}
	subs, _ := st.DownloadedSubs(1)
	if free, err := st.DeleteDownloadedSub(subs[0].ID); err != nil || free {
		t.Fatalf("first delete: free=%v err=%v", free, err)
	}
	if free, err := st.DeleteDownloadedSub(subs[1].ID); err != nil || !free {
		t.Fatalf("second delete: free=%v err=%v", free, err)
	}
	if _, err := st.DeleteDownloadedSub(subs[1].ID); err != ErrNotFound {
		t.Fatalf("deleting a missing row: %v", err)
	}
}

func TestDownloadedSubsMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "lex.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The table as created by earlier versions.
	if _, err := db.Exec(`CREATE TABLE downloaded_subs (
		id INTEGER PRIMARY KEY,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		language TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		path TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		hearing_impaired INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 { // and opening an already migrated database is a no-op
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.DownloadedSubs(1); err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
}
