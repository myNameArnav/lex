package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DownloadedSub is a subtitle fetched from an online provider. They live in
// the data folder (media may be read-only) and are listed as external
// subtitles with index 2000+ID.
type DownloadedSub struct {
	ID              int64
	FileID          int64
	Language        string
	Title           string
	Path            string
	Provider        string
	HearingImpaired bool
	// UserID is who downloaded it (0 for rows from before this was
	// recorded); only they or an admin may delete it.
	UserID int64
}

const DownloadedSubBase = 2000

// downloadedSubSeq (in kv) is the highest ID ever handed out. The table
// predates AUTOINCREMENT, so SQLite would otherwise reuse the ID of a deleted
// newest row, and with it the subtitle index that caches (server-side VTT,
// browsers) are keyed on.
const downloadedSubSeq = "downloaded_subs.seq"

// migrateDownloadedSubs adds columns that newer versions expect to databases
// created before them.
func migrateDownloadedSubs(db *sql.DB) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('downloaded_subs')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == "user_id" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(`ALTER TABLE downloaded_subs ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`)
	return err
}

const downloadedSubCols = `id,file_id,language,title,path,provider,hearing_impaired,user_id`

func scanDownloadedSub(sc interface{ Scan(...any) error }, d *DownloadedSub) error {
	return sc.Scan(&d.ID, &d.FileID, &d.Language, &d.Title, &d.Path, &d.Provider, &d.HearingImpaired, &d.UserID)
}

// AddDownloadedSub records a downloaded subtitle and sets d.ID. If the same
// subtitle file (same path) is already recorded for this media file, that
// row is reused instead: d is filled from it and created is false.
func (s *Store) AddDownloadedSub(d *DownloadedSub) (created bool, err error) {
	err = s.Tx(context.Background(), func(tx *sql.Tx) error {
		// Write first so this transaction holds the write lock before it
		// reads; a read-then-write transaction can fail with SQLITE_BUSY if
		// another connection commits in between.
		if _, err := tx.Exec(`INSERT INTO kv(key,value) VALUES(?,'0') ON CONFLICT(key) DO NOTHING`, downloadedSubSeq); err != nil {
			return err
		}
		var old DownloadedSub
		err := scanDownloadedSub(tx.QueryRow(`SELECT `+downloadedSubCols+` FROM downloaded_subs WHERE file_id=? AND path=? ORDER BY id LIMIT 1`, d.FileID, d.Path), &old)
		if err == nil {
			*d = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var seq, top int64
		if err := tx.QueryRow(`SELECT CAST(value AS INTEGER) FROM kv WHERE key=?`, downloadedSubSeq).Scan(&seq); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM downloaded_subs`).Scan(&top); err != nil {
			return err
		}
		d.ID = max(seq, top) + 1
		if _, err := tx.Exec(`INSERT INTO downloaded_subs(id,file_id,language,title,path,provider,hearing_impaired,user_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			d.ID, d.FileID, d.Language, d.Title, d.Path, d.Provider, d.HearingImpaired, d.UserID, time.Now().Unix()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE kv SET value=? WHERE key=?`, d.ID, downloadedSubSeq); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

func (s *Store) DownloadedSubs(fileID int64) ([]DownloadedSub, error) {
	rows, err := s.db.Query(`SELECT `+downloadedSubCols+` FROM downloaded_subs WHERE file_id=? ORDER BY id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadedSub
	for rows.Next() {
		var d DownloadedSub
		if err := scanDownloadedSub(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DownloadedSubByPath returns the row recording the subtitle file at path
// for a media file.
func (s *Store) DownloadedSubByPath(fileID int64, path string) (*DownloadedSub, error) {
	var d DownloadedSub
	err := scanDownloadedSub(s.db.QueryRow(`SELECT `+downloadedSubCols+` FROM downloaded_subs WHERE file_id=? AND path=? ORDER BY id LIMIT 1`, fileID, path), &d)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) DownloadedSub(id int64) (*DownloadedSub, error) {
	var d DownloadedSub
	err := scanDownloadedSub(s.db.QueryRow(`SELECT `+downloadedSubCols+` FROM downloaded_subs WHERE id=?`, id), &d)
	if err != nil {
		return nil, ErrNotFound
	}
	return &d, nil
}

// DeleteDownloadedSub removes a row and reports whether its file is no
// longer referenced by any other row (older versions could record the same
// download twice), i.e. whether the caller may delete it from disk.
func (s *Store) DeleteDownloadedSub(id int64) (pathFree bool, err error) {
	err = s.Tx(context.Background(), func(tx *sql.Tx) error {
		var path string
		if err := tx.QueryRow(`DELETE FROM downloaded_subs WHERE id=? RETURNING path`, id).Scan(&path); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM downloaded_subs WHERE path=?`, path).Scan(&n); err != nil {
			return err
		}
		pathFree = n == 0
		return nil
	})
	return pathFree, err
}
