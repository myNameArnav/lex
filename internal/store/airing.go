package store

import (
	"database/sql"
)

// Airable is a movie or episode with a known duration, for Live TV.
type Airable struct {
	ItemID    int64
	LibraryID int64
	ShowID    int64 // 0 for movies
	Season    int
	Duration  float64
}

// AirFilter picks what a Live TV channel airs: titles in any of Libraries,
// with any of Genres, of Kind ("movie", "show" or "" for both), where an
// empty filter doesn't limit; or, with Kind "picked", just Titles. Exclude
// leaves titles out (a picked channel passes none). Titles are movies and shows (item ids).
type AirFilter struct {
	Libraries []int64
	Genres    []string
	Kind      string
	Titles    []int64
	Exclude   []int64
}

// Airables lists the movies and episodes with a probed duration of at least
// a minute (no samples or trailers) that match f (an episode has its show's
// genres), in id order. The duration is the one of the file players pick
// first (ItemFiles' order).
func (s *Store) Airables(f AirFilter) ([]Airable, error) {
	// Genres are matched decoded: stored JSON escapes e.g. "&".
	q := `SELECT id, library_id, show_id, season, dur FROM (SELECT i.id, i.library_id, i.show_id, i.season,
			(SELECT f.duration FROM files f WHERE f.item_id=i.id AND f.extra='' ORDER BY f.height DESC, f.size DESC LIMIT 1) dur
		FROM items i LEFT JOIN items sh ON sh.id=i.show_id AND i.kind='episode'
		WHERE i.kind IN ('movie','episode')
		AND CASE :kind WHEN '' THEN 1 WHEN 'picked' THEN (CASE i.kind WHEN 'episode' THEN i.show_id ELSE i.id END) IN (SELECT value FROM json_each(:titles))
			ELSE (:kind='movie')=(i.kind='movie') END
		AND (CASE i.kind WHEN 'episode' THEN i.show_id ELSE i.id END) NOT IN (SELECT value FROM json_each(:exclude))
		AND (:libs='[]' OR i.library_id IN (SELECT value FROM json_each(:libs)))
		AND (:genres='[]' OR EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(COALESCE(sh.genres,i.genres)) THEN COALESCE(sh.genres,i.genres) ELSE '[]' END) j
			WHERE j.value IN (SELECT value FROM json_each(:genres)))))
		WHERE dur>=60 ORDER BY id`
	list := func(v any, n int) string {
		if n == 0 {
			return "[]"
		}
		return jsonString(v)
	}
	libs, genres := list(f.Libraries, len(f.Libraries)), list(f.Genres, len(f.Genres))
	rows, err := s.db.Query(q, sql.Named("kind", f.Kind), sql.Named("libs", libs), sql.Named("genres", genres),
		sql.Named("titles", list(f.Titles, len(f.Titles))), sql.Named("exclude", list(f.Exclude, len(f.Exclude))))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Airable
	for rows.Next() {
		var a Airable
		if err := rows.Scan(&a.ItemID, &a.LibraryID, &a.ShowID, &a.Season, &a.Duration); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
