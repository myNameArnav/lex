package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Backups are consistent snapshots of lex.db made with VACUUM INTO, which is
// safe while Lex runs (it reads one transaction's view of the WAL database).
// They hold users, watch history, matches and settings. Caches (artwork,
// subtitles, previews) are rebuilt on demand and aren't backed up.
//
// Each backup is one file named lex-<UTC time>-<reason>.db, written to a
// .part file first so a half-written backup is never listed.

// Backup reasons.
const (
	BackupDaily        = "daily"
	BackupManual       = "manual"
	BackupPreMigration = "pre-migration"
	BackupPreRestore   = "pre-restore"
)

var backupReasons = []string{BackupDaily, BackupManual, BackupPreMigration, BackupPreRestore}

const backupTimeFormat = "20060102-150405"

var backupName = regexp.MustCompile(`^lex-(\d{8}-\d{6})-(daily|manual|pre-migration|pre-restore)\.db$`)

// ErrBackupExists means a backup of the same kind was made this second.
var ErrBackupExists = errors.New("a backup was just made; try again in a moment")

// BackupInfo describes one backup file.
type BackupInfo struct {
	Name    string `json:"name"`
	Reason  string `json:"reason"`
	Created int64  `json:"created"` // unix seconds
	Size    int64  `json:"size"`
}

// BackupDir is the folder backups go to: <data>/backups, or a "lex-backups"
// folder inside the configured one (so Lex never mixes its files with
// others, the same as the SSD cache).
func BackupDir(dataDir, configured string) string {
	if d := strings.TrimSpace(configured); d != "" {
		return filepath.Join(d, "lex-backups")
	}
	return filepath.Join(dataDir, "backups")
}

// ValidBackupName reports whether name is a backup file Lex made (and so is
// safe to join to the backup folder).
func ValidBackupName(name string) bool { return backupName.MatchString(name) }

// Backup snapshots the database into dir.
func (s *Store) Backup(dir, reason string) (BackupInfo, error) {
	return snapshot(context.Background(), s.db, dir, reason, time.Now())
}

func snapshot(ctx context.Context, db *sql.DB, dir, reason string, at time.Time) (BackupInfo, error) {
	if !validReason(reason) {
		return BackupInfo{}, fmt.Errorf("unknown backup reason %q", reason)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupInfo{}, err
	}
	name := fmt.Sprintf("lex-%s-%s.db", at.UTC().Format(backupTimeFormat), reason)
	final := filepath.Join(dir, name)
	if _, err := os.Stat(final); err == nil {
		return BackupInfo{}, ErrBackupExists
	}
	part := final + ".part"
	// VACUUM INTO won't overwrite; a leftover from a crash goes.
	os.Remove(part)
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, part); err != nil {
		os.Remove(part)
		return BackupInfo{}, err
	}
	if err := os.Chmod(part, 0o600); err != nil {
		os.Remove(part)
		return BackupInfo{}, err
	}
	if err := syncFile(part); err != nil {
		os.Remove(part)
		return BackupInfo{}, err
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return BackupInfo{}, err
	}
	fi, err := os.Stat(final)
	if err != nil {
		return BackupInfo{}, err
	}
	return BackupInfo{Name: name, Reason: reason, Created: at.Unix(), Size: fi.Size()}, nil
}

func validReason(r string) bool {
	for _, x := range backupReasons {
		if x == r {
			return true
		}
	}
	return false
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// ListBackups returns the backups in dir, newest first. A missing folder
// has none.
func ListBackups(dir string) ([]BackupInfo, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []BackupInfo{}
	for _, e := range ents {
		m := backupName.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		t, err := time.Parse(backupTimeFormat, m[1])
		if err != nil {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{Name: e.Name(), Reason: m[2], Created: t.Unix(), Size: fi.Size()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

// PruneBackups keeps the newest keep backups of each kind in dir and deletes
// the rest. It returns how many it deleted.
func PruneBackups(dir string, keep int) (int, error) {
	if keep < 1 {
		keep = 1
	}
	list, err := ListBackups(dir)
	if err != nil {
		return 0, err
	}
	seen := map[string]int{}
	n := 0
	for _, b := range list {
		seen[b.Reason]++
		if seen[b.Reason] <= keep {
			continue
		}
		if err := os.Remove(filepath.Join(dir, b.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		n++
	}
	return n, nil
}

// configuredBackupDir reads the backup folder setting straight from the
// config row, for use before the Store is set up (migrations, restore). Any
// problem means the default folder.
func configuredBackupDir(db *sql.DB, dataDir string) string {
	var raw string
	var c struct {
		BackupDir string `json:"backupDir"`
	}
	if err := db.QueryRow(`SELECT value FROM kv WHERE key='config'`).Scan(&raw); err == nil {
		json.Unmarshal([]byte(raw), &c)
	}
	return BackupDir(dataDir, c.BackupDir)
}

// backupBeforeMigrating snapshots a database that has migrations to apply.
// A new, empty database has nothing to lose and isn't backed up.
func backupBeforeMigrating(ctx context.Context, db *sql.DB, dataDir string) (string, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return "", err
	}
	if version >= len(migrations) {
		return "", nil
	}
	if version == 0 {
		var tables int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table'`).Scan(&tables); err != nil {
			return "", err
		}
		if tables == 0 {
			return "", nil
		}
	}
	dir := configuredBackupDir(db, dataDir)
	b, err := snapshot(ctx, db, dir, BackupPreMigration, time.Now())
	if errors.Is(err, ErrBackupExists) {
		// Restarted within the same second: that backup will do.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, b.Name), nil
}

func openRaw(path string, readOnly bool) (*sql.DB, error) {
	q := "?_pragma=busy_timeout(10000)"
	if readOnly {
		q += "&mode=ro"
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+q)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// CheckBackup opens a backup read-only and checks that it is an intact Lex
// database this version can run.
func CheckBackup(path string) error {
	db, err := openRaw(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		return fmt.Errorf("not a readable SQLite database: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("integrity check failed: %s", res)
	}
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='users'`).Scan(&users); err != nil {
		return err
	}
	if users == 0 {
		return errors.New("not a lex database (no users table)")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("%w (backup is at schema version %d, this binary knows up to %d)", ErrSchemaTooNew, version, len(migrations))
	}
	return nil
}

// Restore replaces <dataDir>/lex.db with the backup at src. Lex must not be
// running. The current database is first backed up (reason pre-restore), and
// the path of that backup is returned ("" when there was no database).
func Restore(dataDir, src string) (string, error) {
	if err := CheckBackup(src); err != nil {
		return "", err
	}
	dbPath := filepath.Join(dataDir, "lex.db")
	prev := ""
	if fi, err := os.Stat(dbPath); err == nil && fi.Size() > 0 {
		db, err := openRaw(dbPath, false)
		if err != nil {
			return "", err
		}
		dir := configuredBackupDir(db, dataDir)
		b, err := snapshot(context.Background(), db, dir, BackupPreRestore, time.Now())
		if err == nil {
			prev = filepath.Join(dir, b.Name)
			// Fold the WAL into lex.db and empty it, so no frames of the
			// old database can be replayed onto the restored one.
			_, err = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		}
		db.Close()
		if err != nil {
			return "", fmt.Errorf("back up the current database: %w", err)
		}
	}
	tmp := dbPath + ".restore"
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return prev, err
	}
	for _, suf := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suf); err != nil && !errors.Is(err, os.ErrNotExist) {
			os.Remove(tmp)
			return prev, err
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		os.Remove(tmp)
		return prev, err
	}
	return prev, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
