package store

import "time"

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
}

const DownloadedSubBase = 2000

func (s *Store) AddDownloadedSub(d *DownloadedSub) error {
	res, err := s.db.Exec(`INSERT INTO downloaded_subs(file_id,language,title,path,provider,hearing_impaired,created_at) VALUES(?,?,?,?,?,?,?)`,
		d.FileID, d.Language, d.Title, d.Path, d.Provider, d.HearingImpaired, time.Now().Unix())
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) DownloadedSubs(fileID int64) ([]DownloadedSub, error) {
	rows, err := s.db.Query(`SELECT id,file_id,language,title,path,provider,hearing_impaired FROM downloaded_subs WHERE file_id=? ORDER BY id`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadedSub
	for rows.Next() {
		var d DownloadedSub
		if err := rows.Scan(&d.ID, &d.FileID, &d.Language, &d.Title, &d.Path, &d.Provider, &d.HearingImpaired); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DownloadedSub(id int64) (*DownloadedSub, error) {
	var d DownloadedSub
	err := s.db.QueryRow(`SELECT id,file_id,language,title,path,provider,hearing_impaired FROM downloaded_subs WHERE id=?`, id).
		Scan(&d.ID, &d.FileID, &d.Language, &d.Title, &d.Path, &d.Provider, &d.HearingImpaired)
	if err != nil {
		return nil, ErrNotFound
	}
	return &d, nil
}

func (s *Store) DeleteDownloadedSub(id int64) error {
	_, err := s.db.Exec(`DELETE FROM downloaded_subs WHERE id=?`, id)
	return err
}
