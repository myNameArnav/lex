package store

import (
	"errors"
	"strings"
)

type Library struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // movies | shows | mixed
	Paths     []string `json:"paths"`
	CreatedAt int64    `json:"createdAt"`
	LastScan  int64    `json:"lastScan"`
	ItemCount int      `json:"itemCount"`
}

func (s *Store) Libraries() ([]Library, error) {
	rows, err := s.db.Query(`SELECT l.id,l.name,l.kind,l.paths,l.created_at,l.last_scan,
		(SELECT COUNT(*) FROM items i WHERE i.library_id=l.id AND i.parent_id IS NULL)
		FROM libraries l ORDER BY l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		var paths string
		if err := rows.Scan(&l.ID, &l.Name, &l.Kind, &paths, &l.CreatedAt, &l.LastScan, &l.ItemCount); err != nil {
			return nil, err
		}
		jsonInto(paths, &l.Paths)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Library(id int64) (*Library, error) {
	libs, err := s.Libraries()
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.ID == id {
			return &l, nil
		}
	}
	return nil, ErrNotFound
}

func validKind(k string) bool { return k == "movies" || k == "shows" || k == "mixed" }

func (s *Store) CreateLibrary(name, kind string, paths []string) (*Library, error) {
	if strings.TrimSpace(name) == "" || !validKind(kind) || len(paths) == 0 {
		return nil, errors.New("name, kind (movies|shows|mixed) and at least one path are required")
	}
	res, err := s.db.Exec(`INSERT INTO libraries(name,kind,paths,created_at) VALUES(?,?,?,?)`, strings.TrimSpace(name), kind, jsonString(paths), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Library(id)
}

func (s *Store) UpdateLibrary(id int64, name, kind string, paths []string) error {
	if strings.TrimSpace(name) == "" || !validKind(kind) || len(paths) == 0 {
		return errors.New("name, kind and paths are required")
	}
	_, err := s.db.Exec(`UPDATE libraries SET name=?,kind=?,paths=? WHERE id=?`, strings.TrimSpace(name), kind, jsonString(paths), id)
	return err
}

func (s *Store) DeleteLibrary(id int64) error {
	_, err := s.db.Exec(`DELETE FROM libraries WHERE id=?`, id)
	return err
}

func (s *Store) SetLibraryScanned(id int64) error {
	_, err := s.db.Exec(`UPDATE libraries SET last_scan=? WHERE id=?`, now(), id)
	return err
}

// ---- items ----
