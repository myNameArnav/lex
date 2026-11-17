package store

import (
	"reflect"
	"testing"
)

func fileFacts(t *testing.T, st *Store, id int64) (int, []string) {
	t.Helper()
	var depth int
	if err := st.DB().QueryRow(`SELECT vdepth FROM files WHERE id=?`, id).Scan(&depth); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB().Query(`SELECT codec FROM file_audio WHERE file_id=? ORDER BY codec`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	codecs := []string{}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		codecs = append(codecs, c)
	}
	return depth, codecs
}

// The facts follow files.info through probes, re-probes and deletes.
func TestFileFactsFollowInfo(t *testing.T) {
	st := testStore(t)
	lib, _ := st.CreateLibrary("Movies", "movies", []string{"/m"})
	item, _ := st.InsertItem(&Item{LibraryID: lib.ID, Kind: "movie", Title: "Dune", Path: "/m/Dune"})
	id, err := st.InsertFile(&File{ItemID: item, LibraryID: lib.ID, Path: "/m/Dune.mkv", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if d, c := fileFacts(t, st, id); d != 0 || len(c) != 0 {
		t.Fatalf("unprobed: %d %v", d, c)
	}
	info := &MediaInfo{Streams: []Stream{
		{Index: 0, Type: "video", Codec: "hevc", BitDepth: 10},
		{Index: 1, Type: "audio", Codec: "eac3"}, {Index: 2, Type: "audio", Codec: "eac3"}, {Index: 3, Type: "audio", Codec: "dts"},
		{Index: 4, Type: "audio"},
		{Index: 5, Type: "video", Codec: "mjpeg", BitDepth: 8},
	}}
	if err := st.SaveProbe(id, info, ""); err != nil {
		t.Fatal(err)
	}
	if d, c := fileFacts(t, st, id); d != 10 || !reflect.DeepEqual(c, []string{"dts", "eac3", "unknown"}) {
		t.Fatalf("probed: %d %v", d, c)
	}
	// A re-probe replaces them (the file was swapped for another version).
	info.Streams = []Stream{{Index: 0, Type: "video", Codec: "h264"}, {Index: 1, Type: "audio", Codec: "aac"}}
	st.SaveProbe(id, info, "")
	if d, c := fileFacts(t, st, id); d != 0 || !reflect.DeepEqual(c, []string{"aac"}) {
		t.Fatalf("re-probed: %d %v", d, c)
	}
	if _, err := st.DB().Exec(`DELETE FROM files WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM file_audio`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d audio rows left after delete", n)
	}
}

// Migration 3 fills in the facts of files probed by an older Lex.
func TestMigrationBackfillsFileFacts(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	db := rawDB(t, dir)
	execAll(t, db, `UPDATE files SET info='{"streams":[{"type":"audio","codec":"aac"},{"type":"video","codec":"hevc","bitDepth":10},{"type":"audio","codec":"truehd"}]}' WHERE id=1`)
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if d, c := fileFacts(t, st, 1); d != 10 || !reflect.DeepEqual(c, []string{"aac", "truehd"}) {
		t.Fatalf("backfilled: %d %v", d, c)
	}
	ls, err := st.LibraryStats()
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.AudioCodecs) != 2 {
		t.Fatalf("audio buckets %+v", ls.AudioCodecs)
	}
}
