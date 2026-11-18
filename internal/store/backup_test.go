package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupAndList(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateUser("ana", "secret-password", true); err != nil {
		t.Fatal(err)
	}
	bdir := BackupDir(dir, "")
	// A new database has nothing to migrate, so nothing was backed up.
	if list, _ := ListBackups(bdir); len(list) != 0 {
		t.Fatalf("fresh database backed up: %+v", list)
	}
	b, err := st.Backup(bdir, BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidBackupName(b.Name) || b.Reason != BackupManual || b.Size == 0 {
		t.Fatalf("backup = %+v", b)
	}
	// Same name as the first (the race detector can push a second call
	// past a second boundary, so reuse the first one's timestamp).
	if _, err := snapshot(context.Background(), st.db, bdir, BackupManual, time.Unix(b.Created, 0)); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("second backup in the same second: err = %v", err)
	}
	fi, err := os.Stat(filepath.Join(bdir, b.Name))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
	if err := CheckBackup(filepath.Join(bdir, b.Name)); err != nil {
		t.Fatal(err)
	}
	// The snapshot holds the user.
	db := rawDB(t, bdir+"-copy")
	db.Close()
	if err := copyFile(filepath.Join(bdir, b.Name), filepath.Join(bdir+"-copy", "lex.db")); err != nil {
		t.Fatal(err)
	}
	snap, err := Open(bdir + "-copy")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if u, err := snap.Users(); err != nil || len(u) != 1 || u[0].Name != "ana" {
		t.Fatalf("users in backup = %+v, %v", u, err)
	}
	// Strays and partial files aren't listed.
	os.WriteFile(filepath.Join(bdir, "notes.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(bdir, "lex-20250101-000000-daily.db.part"), []byte("x"), 0o600)
	list, err := ListBackups(bdir)
	if err != nil || len(list) != 1 || list[0].Name != b.Name {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestPruneBackupsKeepsNewestOfEachKind(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bdir := t.TempDir()
	base := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	for i := range 5 {
		if _, err := snapshot(t.Context(), st.DB(), bdir, BackupDaily, base.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := snapshot(t.Context(), st.DB(), bdir, BackupPreMigration, base); err != nil {
		t.Fatal(err)
	}
	n, err := PruneBackups(bdir, 2)
	if err != nil || n != 3 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	list, _ := ListBackups(bdir)
	var names []string
	for _, b := range list {
		names = append(names, b.Name)
	}
	want := []string{"lex-20260105-030000-daily.db", "lex-20260104-030000-daily.db", "lex-20260101-030000-pre-migration.db"}
	if len(names) != len(want) {
		t.Fatalf("kept %q, want %q", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("kept %q, want %q", names, want)
		}
	}
}

func TestBackupBeforeMigrating(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	list, err := ListBackups(BackupDir(dir, ""))
	if err != nil || len(list) != 1 || list[0].Reason != BackupPreMigration {
		t.Fatalf("backups = %+v, %v", list, err)
	}
	// The backup is the database as it was: still at version 0.
	p := filepath.Join(BackupDir(dir, ""), list[0].Name)
	db, err := openRaw(p, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := userVersion(t, db); v != 0 {
		t.Fatalf("backup user_version = %d, want 0", v)
	}
	// Reopening an up-to-date database makes no more backups.
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if list, _ := ListBackups(BackupDir(dir, "")); len(list) != 1 {
		t.Fatalf("backups after reopen = %+v", list)
	}
}

func TestBackupBeforeMigratingUsesConfiguredFolder(t *testing.T) {
	dir := t.TempDir()
	oldFixture(t, dir)
	other := t.TempDir()
	db := rawDB(t, dir)
	execAll(t, db, `INSERT INTO kv(key,value) VALUES('config', '{"backupDir":"`+other+`"}')`)
	db.Close()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if list, _ := ListBackups(filepath.Join(other, "lex-backups")); len(list) != 1 {
		t.Fatalf("backups in configured folder = %+v", list)
	}
}

func TestRestore(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("before", "secret-password", true); err != nil {
		t.Fatal(err)
	}
	b, err := st.Backup(BackupDir(dir, ""), BackupManual)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("after", "secret-password", false); err != nil {
		t.Fatal(err)
	}
	st.Close()

	src := filepath.Join(BackupDir(dir, ""), b.Name)
	prev, err := Restore(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	if prev == "" || CheckBackup(prev) != nil {
		t.Fatalf("pre-restore backup %q", prev)
	}
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	users, err := st.Users()
	if err != nil || len(users) != 1 || users[0].Name != "before" {
		t.Fatalf("users after restore = %+v, %v", users, err)
	}
}

func TestRestoreRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(t.TempDir(), "junk.db")
	os.WriteFile(junk, []byte("not a database at all, just some text padding it out"), 0o600)
	if _, err := Restore(dir, junk); err == nil {
		t.Fatal("restored a non-database")
	}
	other := filepath.Join(t.TempDir(), "other.db")
	db := rawDB(t, filepath.Dir(other))
	execAll(t, db, `CREATE TABLE t(x)`)
	db.Close()
	if _, err := Restore(dir, filepath.Join(filepath.Dir(other), "lex.db")); err == nil {
		t.Fatal("restored a database that isn't Lex's")
	}
	if _, err := os.Stat(filepath.Join(dir, "lex.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected restore wrote lex.db: %v", err)
	}
}
