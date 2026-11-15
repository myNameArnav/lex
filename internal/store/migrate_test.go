package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// rawDB opens dataDir's database without running migrations.
func rawDB(t *testing.T, dataDir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "lex.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func setUserVersion(t *testing.T, db *sql.DB, v int) {
	t.Helper()
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, v)); err != nil {
		t.Fatal(err)
	}
}

func execAll(t *testing.T, db *sql.DB, qs ...string) {
	t.Helper()
	for _, q := range qs {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// schemaShape lists every table's columns and every index, independent of
// column order and of how the CREATE statements were written.
func schemaShape(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, tbl_name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	var out, tables []string
	for rows.Next() {
		var typ, name, tbl string
		if err := rows.Scan(&typ, &name, &tbl); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ+" "+name+" on "+tbl)
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	rows.Close()
	for _, tbl := range tables {
		cols, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value,''), pk FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var name, typ, dflt string
			var notnull, pk int
			if err := cols.Scan(&name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("column %s.%s %s notnull=%d default=%q pk=%d", tbl, name, typ, notnull, dflt, pk))
		}
		cols.Close()
	}
	sort.Strings(out)
	return out
}

func freshShape(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return schemaShape(t, rawDB(t, dir))
}

func TestMigrateFreshDB(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, st.DB()); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	for _, c := range []struct{ table, column string }{{"files", "probe_attempts"}, {"downloaded_subs", "user_id"}, {"intro_scans", "result"}} {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s.%s: n=%d err=%v", c.table, c.column, n, err)
		}
	}
	// The migration connection went back to the pool; none may be left
	// with foreign keys off.
	ctx := context.Background()
	var conns []*sql.Conn
	for range 4 {
		c, err := st.DB().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("connection %d: foreign_keys=%d err=%v", len(conns), fk, err)
		}
	}
	for _, c := range conns {
		c.Close()
	}
	st.Close()
}

// oldFixture writes a database as Lex c4033b6 left it: no
// downloaded_subs.user_id, no files.probe_attempts, user_version 0.
func oldFixture(t *testing.T, dir string) {
	t.Helper()
	old, err := os.ReadFile(filepath.Join("testdata", "schema_c4033b6.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	execAll(t, db, string(old),
		`INSERT INTO users(id,name,pass_hash,is_admin,created_at) VALUES(1,'admin','x',1,0)`,
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`,
		`INSERT INTO items(id,library_id,kind,title,sort_title,added_at,updated_at) VALUES(1,1,'movie','Dune','dune',0,0)`,
		`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at,probe_error) VALUES(1,1,1,'/m/dune.mkv',1,1,0,'boom')`,
		`INSERT INTO downloaded_subs(id,file_id,language,path,created_at) VALUES(7,1,'eng','/d/1-7.srt',0)`,
		`INSERT INTO user_data(user_id,item_id,position) VALUES(1,1,42)`,
	)
	for _, c := range []struct{ table, column string }{{"files", "probe_attempts"}, {"downloaded_subs", "user_id"}} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column).Scan(&n); err != nil || n != 0 {
			t.Fatalf("fixture %s.%s: n=%d err=%v", c.table, c.column, n, err)
		}
	}
	db.Close()
}

func checkOldFixtureData(t *testing.T, st *Store) {
	t.Helper()
	var attempts int
	var probeErr string
	if err := st.DB().QueryRow(`SELECT probe_attempts, probe_error FROM files WHERE id=1`).Scan(&attempts, &probeErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || probeErr != "boom" {
		t.Fatalf("file: attempts=%d probe_error=%q", attempts, probeErr)
	}
	subs, err := st.DownloadedSubs(1)
	if err != nil || len(subs) != 1 || subs[0].ID != 7 || subs[0].UserID != 0 || subs[0].Language != "eng" {
		t.Fatalf("downloaded subs = %+v, err %v", subs, err)
	}
	var pos float64
	if err := st.DB().QueryRow(`SELECT position FROM user_data WHERE user_id=1 AND item_id=1`).Scan(&pos); err != nil || pos != 42 {
		t.Fatalf("user_data position = %v, err %v", pos, err)
	}
}

func TestMigrateOldSchema(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, st.DB()); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	checkOldFixtureData(t, st)
	st.Close()
	if got, want := schemaShape(t, rawDB(t, dir)), freshShape(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated schema differs from a fresh one:\n got %q\nwant %q", got, want)
	}
}

func TestMigrateReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	for i := range 3 {
		st, err := Open(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		checkOldFixtureData(t, st)
		st.Close()
	}
	db := rawDB(t, dir)
	if v := userVersion(t, db); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	// A database from the last unversioned Lex already has every column
	// (probe_attempts, downloaded_subs.user_id) but user_version 0;
	// migration 1 must run over it cleanly.
	setUserVersion(t, db, 0)
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v := userVersion(t, st.DB()); v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
	checkOldFixtureData(t, st)
}

// A database stamped by an earlier build still picks up columns added to
// lateColumns (and tables added to schema) afterwards.
func TestLateColumnsReachVersionedDB(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	db := rawDB(t, dir)
	setUserVersion(t, db, 1)
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	checkOldFixtureData(t, st)
	st.Close()
	if got, want := schemaShape(t, rawDB(t, dir)), freshShape(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("schema differs from a fresh one:\n got %q\nwant %q", got, want)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	future := len(migrations) + 1
	db := rawDB(t, dir)
	setUserVersion(t, db, future)
	before := schemaShape(t, db)
	db.Close()

	st, err = Open(dir)
	if err == nil {
		st.Close()
		t.Fatal("Open succeeded on a database from a newer version")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	t.Log(err)

	db = rawDB(t, dir)
	if v := userVersion(t, db); v != future {
		t.Fatalf("user_version changed to %d", v)
	}
	if after := schemaShape(t, db); !reflect.DeepEqual(before, after) {
		t.Fatal("schema changed despite refusing to start")
	}
}
