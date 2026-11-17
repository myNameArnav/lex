package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lex/internal/store"
)

func TestBackupEndpoints(t *testing.T) {
	s, h := securityServer(t)
	s.DataDir = t.TempDir()
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	viewerToken, _ := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")

	if w := adminRequest(t, h, viewerToken, "POST", "/api/admin/backups", ""); w.Code != 403 {
		t.Fatalf("viewer backup: %d", w.Code)
	}
	w := adminRequest(t, h, token, "POST", "/api/admin/backups", "")
	if w.Code != 200 {
		t.Fatalf("backup now: %d %s", w.Code, w.Body.String())
	}
	var b store.BackupInfo
	json.Unmarshal(w.Body.Bytes(), &b)
	if b.Reason != store.BackupManual {
		t.Fatalf("backup = %+v", b)
	}

	w = adminRequest(t, h, token, "GET", "/api/admin/backups", "")
	var list struct {
		Dir     string
		Backups []store.BackupInfo
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || list.Dir != filepath.Join(s.DataDir, "backups") || len(list.Backups) != 1 || list.Backups[0].Name != b.Name {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}

	w = adminRequest(t, h, token, "GET", "/api/admin/backups/"+b.Name, "")
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "SQLite format 3") || !strings.Contains(w.Header().Get("Content-Disposition"), b.Name) {
		t.Fatalf("download: %d %q", w.Code, w.Header())
	}
	if w := adminRequest(t, h, viewerToken, "GET", "/api/admin/backups/"+b.Name, ""); w.Code != 403 {
		t.Fatalf("viewer download: %d", w.Code)
	}
	// Only names Lex makes are served, so nothing outside the folder is.
	for _, name := range []string{"..%2flex.db", "lex.db", "lex-20260101-000000-daily.db.part"} {
		if w := adminRequest(t, h, token, "GET", "/api/admin/backups/"+name, ""); w.Code != 404 {
			t.Fatalf("download %s: %d", name, w.Code)
		}
	}

	if w := adminRequest(t, h, token, "DELETE", "/api/admin/backups/"+b.Name, ""); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.DataDir, "backups", b.Name)); !os.IsNotExist(err) {
		t.Fatalf("deleted backup still there: %v", err)
	}
}

func TestBackupFolderSetting(t *testing.T) {
	s, h := securityServer(t)
	s.DataDir = t.TempDir()
	admin, _ := s.St.CreateInitialAdmin("admin", "test-password-123")
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	for _, dir := range []string{"relative/path", filepath.Join(t.TempDir(), "missing")} {
		body, _ := json.Marshal(map[string]string{"backupDir": dir})
		if w := adminRequest(t, h, token, "PUT", "/api/admin/config", string(body)); w.Code != 400 {
			t.Fatalf("backupDir %q: %d", dir, w.Code)
		}
	}
	other := t.TempDir()
	body, _ := json.Marshal(map[string]string{"backupDir": other})
	if w := adminRequest(t, h, token, "PUT", "/api/admin/config", string(body)); w.Code != 200 {
		t.Fatalf("backupDir: %d %s", w.Code, w.Body.String())
	}
	if got, want := s.BackupDir(), filepath.Join(other, "lex-backups"); got != want {
		t.Fatalf("BackupDir = %q, want %q", got, want)
	}
}

func TestBackupIfDue(t *testing.T) {
	s, _ := securityServer(t)
	s.DataDir = t.TempDir()
	s.BackupIfDue()
	list, _ := store.ListBackups(s.BackupDir())
	if len(list) != 1 || list[0].Reason != store.BackupDaily {
		t.Fatalf("first run: %+v", list)
	}
	// Not again until about a day later.
	s.BackupIfDue()
	if list, _ := store.ListBackups(s.BackupDir()); len(list) != 1 {
		t.Fatalf("second run: %+v", list)
	}
	// Age the backup: due again.
	old := filepath.Join(s.BackupDir(), list[0].Name)
	aged := filepath.Join(s.BackupDir(), "lex-"+time.Now().Add(-25*time.Hour).UTC().Format("20060102-150405")+"-daily.db")
	if err := os.Rename(old, aged); err != nil {
		t.Fatal(err)
	}
	s.BackupIfDue()
	if list, _ := store.ListBackups(s.BackupDir()); len(list) != 2 {
		t.Fatalf("after a day: %+v", list)
	}
	// Off: no backups.
	c := s.St.Config()
	c.BackupDaily = false
	s.St.SaveConfig(c)
	os.RemoveAll(s.BackupDir())
	s.BackupIfDue()
	if list, _ := store.ListBackups(s.BackupDir()); len(list) != 0 {
		t.Fatalf("daily off: %+v", list)
	}
}
