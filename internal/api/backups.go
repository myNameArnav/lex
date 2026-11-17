package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"lex/internal/store"
)

// BackupDir is where database backups go.
func (s *Server) BackupDir() string { return store.BackupDir(s.DataDir, s.St.Config().BackupDir) }

// checkBackupDir returns why a backup folder setting can't be saved, or "".
// The folder must already exist (a missing mount shouldn't quietly fill the
// SD card instead); Lex makes its own "lex-backups" folder inside it.
func checkBackupDir(dir, old string) string {
	if dir == "" || dir == old {
		return ""
	}
	if !filepath.IsAbs(dir) {
		return "Backup folder must be an absolute path"
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "Backup folder doesn't exist or isn't a folder: " + dir
	}
	return ""
}

// backup makes a backup and then prunes the folder to the configured count.
func (s *Server) backup(reason string) (store.BackupInfo, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	dir := s.BackupDir()
	b, err := s.St.Backup(dir, reason)
	if err != nil {
		return b, err
	}
	if n, err := store.PruneBackups(dir, s.St.Config().BackupKeep); err != nil {
		s.Log.Warnf("prune backups: %v", err)
	} else if n > 0 {
		s.Log.Debugf("pruned %d old backups", n)
	}
	return b, nil
}

// BackupIfDue makes the daily backup when it is on and the newest daily
// backup is about a day old or missing. main calls it periodically.
func (s *Server) BackupIfDue() {
	if !s.St.Config().BackupDaily {
		return
	}
	list, err := store.ListBackups(s.BackupDir())
	if err == nil {
		for _, b := range list {
			if b.Reason == store.BackupDaily {
				// A little under a day, so an hourly check doesn't drift
				// later by up to an hour each day.
				if time.Since(time.Unix(b.Created, 0)) < 23*time.Hour {
					return
				}
				break
			}
		}
	}
	start := time.Now()
	b, err := s.backup(store.BackupDaily)
	s.backupMu.Lock()
	if err != nil {
		s.backupErr = err.Error()
	} else {
		s.backupErr = ""
	}
	s.backupMu.Unlock()
	if err != nil {
		s.Log.Errorf("daily database backup failed: %v", err)
		return
	}
	s.Log.Infof("backed up the database to %s (%d KB, %s)", b.Name, b.Size>>10, time.Since(start).Round(time.Millisecond))
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	dir := s.BackupDir()
	list, err := store.ListBackups(dir)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.backupMu.Lock()
	lastErr := s.backupErr
	s.backupMu.Unlock()
	writeJSON(w, map[string]any{"dir": dir, "backups": list, "lastError": lastErr})
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.backup(store.BackupManual)
	if errors.Is(err, store.ErrBackupExists) {
		writeErr(w, 409, err.Error())
		return
	}
	if err != nil {
		s.Log.Errorf("database backup failed: %v", err)
		writeErr(w, 500, "Backup failed: "+err.Error())
		return
	}
	s.Log.Infof("database backed up to %s by %s", b.Name, userOf(r).Name)
	writeJSON(w, b)
}

func (s *Server) backupPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !store.ValidBackupName(name) {
		writeErr(w, 404, "not found")
		return "", false
	}
	return filepath.Join(s.BackupDir(), name), true
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.backupPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.backupPath(w, r)
	if !ok {
		return
	}
	s.backupMu.Lock()
	err := os.Remove(p)
	s.backupMu.Unlock()
	if errors.Is(err, os.ErrNotExist) {
		writeErr(w, 404, "not found")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Log.Infof("backup %s deleted by %s", filepath.Base(p), userOf(r).Name)
	w.WriteHeader(204)
}
