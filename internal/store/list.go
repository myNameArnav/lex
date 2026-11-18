package store

import (
	"database/sql"
	"fmt"
	"strings"
)

type ListQuery struct {
	LibraryID int64
	Kind      string // movie | show | episode
	Sort      string // title | year | added | rating | played
	Desc      bool
	Genre     string
	Filter    string // unplayed | played | inprogress | favorite | watchlist
	Search    string
	Limit     int
	Offset    int
	Seed      int64 // sort=random: a non-zero seed gives a stable order across pages
}

// listConds is the WHERE conditions (and their arguments) for q's kind,
// library, genre and filter, limited to the libraries user uid may see.
// Search is left to ListItems. The conditions refer to items as i and to
// the user's data as ud.
func listConds(uid int64, q ListQuery) ([]string, []any) {
	var conds []string
	args := []any{sql.Named("uid", uid)}
	conds = append(conds, "i.parent_id IS NULL")
	if q.Kind == "episode" {
		conds = []string{"i.kind='episode'"}
	} else if q.Kind != "" {
		conds = append(conds, "i.kind=:kind")
		args = append(args, sql.Named("kind", q.Kind))
	}
	conds = append(conds, libAccess)
	if q.LibraryID > 0 {
		conds = append(conds, "i.library_id=:lib")
		args = append(args, sql.Named("lib", q.LibraryID))
	}
	if q.Genre != "" {
		// Matched decoded and exactly: stored JSON escapes e.g. "&", and a
		// LIKE pattern would treat % and _ in the name as wildcards.
		conds = append(conds, `EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(i.genres) THEN i.genres ELSE '[]' END) j WHERE j.value=:genre)`)
		args = append(args, sql.Named("genre", q.Genre))
	}
	switch q.Filter {
	case "unplayed":
		conds = append(conds, `CASE WHEN i.kind='show' THEN EXISTS(SELECT 1 FROM items e LEFT JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id AND e.kind='episode' AND COALESCE(u2.played,0)=0) ELSE COALESCE(ud.played,0)=0 END`)
	case "played":
		conds = append(conds, `CASE WHEN i.kind='show' THEN NOT EXISTS(SELECT 1 FROM items e LEFT JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id AND e.kind='episode' AND COALESCE(u2.played,0)=0) ELSE COALESCE(ud.played,0)=1 END`)
	case "inprogress":
		// A show is in progress once any episode is started or watched while
		// some are still unwatched.
		conds = append(conds, `CASE WHEN i.kind='show' THEN EXISTS(SELECT 1 FROM items e JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id AND e.kind='episode' AND (u2.position>0 OR u2.played=1))
			AND EXISTS(SELECT 1 FROM items e LEFT JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id AND e.kind='episode' AND COALESCE(u2.played,0)=0)
			ELSE COALESCE(ud.position,0)>0 END`)
	case "favorite":
		conds = append(conds, "COALESCE(ud.favorite,0)=1")
	case "watchlist":
		conds = append(conds, "EXISTS(SELECT 1 FROM watchlist w WHERE w.user_id=:uid AND w.item_id=i.id)")
	}
	return conds, args
}

// ListItems returns a page of titles as cards (cardCols) and the total.
func (s *Store) ListItems(uid int64, q ListQuery) ([]*Item, int, error) {
	conds, args := listConds(uid, q)
	// The search index matches words by prefix; a term it can't use, or
	// that it finds nothing for, is matched as a substring instead.
	searchAt, fts := -1, ftsQuery(q.Search)
	if q.Search != "" {
		searchAt = len(conds)
		if fts != "" {
			conds = append(conds, ftsCond)
			args = append(args, sql.Named("fts", fts), sql.Named("ftstitle", ftsTitleQuery(fts)))
		} else {
			conds = append(conds, likeCond)
			args = append(args, sql.Named("q", likePattern(q.Search)))
		}
	}
	where := "WHERE " + strings.Join(conds, " AND ")
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items i LEFT JOIN user_data ud ON ud.item_id=i.id AND ud.user_id=:uid `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 && fts != "" {
		fts = ""
		conds[searchAt] = likeCond
		args = append(args, sql.Named("q", likePattern(q.Search)))
		where = "WHERE " + strings.Join(conds, " AND ")
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM items i LEFT JOIN user_data ud ON ud.item_id=i.id AND ud.user_id=:uid `+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	order := "i.sort_title " + dir
	switch q.Sort {
	case "year":
		order = fmt.Sprintf("i.year %s, i.sort_title", dir)
	case "added":
		order = fmt.Sprintf("i.added_at %s, i.id %s", dir, dir)
	case "rating":
		order = fmt.Sprintf("i.rating %s, i.sort_title", dir)
	case "played":
		// Shows have no user data of their own: use their latest episode activity.
		order = fmt.Sprintf("CASE WHEN i.kind='show' THEN COALESCE((SELECT MAX(u2.last_played) FROM items e JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id),0) ELSE COALESCE(ud.last_played,0) END %s, i.sort_title", dir)
	case "premiere":
		order = fmt.Sprintf("i.premiere %s, i.sort_title", dir)
	case "latest":
		order = fmt.Sprintf("CASE WHEN i.kind='show' THEN COALESCE((SELECT MAX(e.added_at) FROM items e WHERE e.show_id=i.id),i.added_at) ELSE i.added_at END %s, i.id %s", dir, dir)
	case "random":
		order = "random()"
		if seed := randomSeed(q.Seed); seed != 0 {
			// A seeded hash of the id: the same order on every page and on Back.
			x := fmt.Sprintf("(i.id * %d %% %d)", seed, randomPrime)
			order = fmt.Sprintf("%s * %s %% %d, i.id", x, x, randomPrime)
		}
	}
	if fts != "" && q.Sort != "random" {
		// Titles that match go before titles found by cast or genre.
		order = ftsTitleFirst + ", " + order
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 500
	}
	where += fmt.Sprintf(" ORDER BY %s LIMIT %d OFFSET %d", order, q.Limit, q.Offset)
	items, err := s.queryItemsUD(udSelectCols(cardCols, where), args...)
	return items, total, err
}

// LetterCounts counts the titles a title-sorted ListItems with q's kind,
// library, genre and filter would list (q.Search is ignored), by the first
// character of their sort title: "a" to "z", "#" for titles that sort before
// "a" (digits, punctuation) and "~" for those that sort after "z" (other
// scripts). Buckets with no titles are left out. Since sort titles are
// lower-cased and compared bytewise, the buckets in that order ("#", "a"…"z",
// "~") are consecutive runs of the title order, so the counts give the offset
// of each letter's first title in either direction.
func (s *Store) LetterCounts(uid int64, q ListQuery) (map[string]int, error) {
	conds, args := listConds(uid, q)
	rows, err := s.db.Query(`SELECT CASE WHEN i.sort_title < 'a' THEN '#' WHEN i.sort_title >= '{' THEN '~' ELSE substr(i.sort_title,1,1) END b, COUNT(*)
		FROM items i LEFT JOIN user_data ud ON ud.item_id=i.id AND ud.user_id=:uid WHERE `+strings.Join(conds, " AND ")+` GROUP BY b`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var b string
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		out[b] = n
	}
	return out, rows.Err()
}

const randomPrime = 2147483647

// randomSeed mixes a client seed into 1..randomPrime-1 (0: unseeded), which
// keeps every product in the random order within 64 bits. Mixing first
// (splitmix64) matters: a seed whose square is small mod the prime would
// otherwise leave the titles in id order.
func randomSeed(seed int64) int64 {
	if seed == 0 {
		return 0
	}
	x := uint64(seed) + 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	x ^= x >> 31
	return int64(x%(randomPrime-1)) + 1
}

// Genres lists the genres of top-level titles in a library (0: all) that
// user uid may see.
func (s *Store) Genres(uid, libID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT j.value FROM items i, json_each(i.genres) j WHERE i.parent_id IS NULL AND (:lib=0 OR i.library_id=:lib) AND `+libAccess+` ORDER BY 1`,
		sql.Named("uid", uid), sql.Named("lib", libID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		rows.Scan(&g)
		out = append(out, g)
	}
	return out, rows.Err()
}

// TopGenres returns up to limit genres that at least min top-level titles
// have, most common first.
func (s *Store) TopGenres(limit, min int) ([]string, error) {
	rows, err := s.db.Query(`SELECT j.value FROM items i, json_each(i.genres) j WHERE i.parent_id IS NULL
		GROUP BY 1 HAVING COUNT(*)>=? ORDER BY COUNT(*) DESC, 1 LIMIT ?`, min, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
