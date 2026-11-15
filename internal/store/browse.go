package store

import (
	"database/sql"
	"errors"
	"strings"
)

// browseSchema holds the per-user watchlist and the movie collections
// (series such as a film trilogy) learned from TMDB or Radarr.
const browseSchema = `
CREATE TABLE IF NOT EXISTS watchlist (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	added_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, item_id)
);
CREATE INDEX IF NOT EXISTS watchlist_recent ON watchlist(user_id, added_at);

CREATE TABLE IF NOT EXISTS collections (
	id INTEGER PRIMARY KEY, -- the TMDB collection id
	name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS item_collections (
	item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
	collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS item_collections_coll ON item_collections(collection_id);
`

// ErrNotWatchlistable: only movies and shows go on a watchlist.
var ErrNotWatchlistable = errors.New("only movies and shows can be added to the watchlist")

// ---- watchlist ----

// SetWatchlist adds a movie or show to a user's watchlist, or takes it off.
func (s *Store) SetWatchlist(uid, itemID int64, on bool) error {
	if !on {
		_, err := s.db.Exec(`DELETE FROM watchlist WHERE user_id=? AND item_id=?`, uid, itemID)
		return err
	}
	it, err := s.Item(itemID)
	if err != nil {
		return err
	}
	if it.Kind != "movie" && it.Kind != "show" {
		return ErrNotWatchlistable
	}
	_, err = s.db.Exec(`INSERT INTO watchlist(user_id,item_id,added_at) VALUES(?,?,?) ON CONFLICT(user_id,item_id) DO NOTHING`, uid, itemID, now())
	return err
}

// Watchlist returns a user's watchlist, most recently added first; limit <=
// 0 returns all of it.
func (s *Store) Watchlist(uid int64, limit int) ([]*Item, error) {
	q := `JOIN watchlist w ON w.item_id=i.id AND w.user_id=:uid ORDER BY w.added_at DESC, i.id DESC`
	args := []any{sql.Named("uid", uid)}
	if limit > 0 {
		q += ` LIMIT :lim`
		args = append(args, sql.Named("lim", limit))
	}
	items, err := s.queryItemsUD(udSelect(q), args...)
	for _, it := range items {
		it.UserData.Watchlist = true
	}
	return items, err
}

// AnnotateWatchlist sets UserData.Watchlist on the movies and shows among
// items that are on the user's watchlist.
func (s *Store) AnnotateWatchlist(uid int64, items ...*Item) error {
	byID := map[int64][]*Item{}
	for _, it := range items {
		if it != nil && (it.Kind == "movie" || it.Kind == "show") {
			byID[it.ID] = append(byID[it.ID], it)
		}
	}
	if len(byID) == 0 {
		return nil
	}
	ids := make([]any, 0, len(byID)+1)
	ids = append(ids, uid)
	for id := range byID {
		ids = append(ids, id)
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(byID)), ",")
	rows, err := s.db.Query(`SELECT item_id FROM watchlist WHERE user_id=? AND item_id IN (`+ph+`)`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		for _, it := range byID[id] {
			if it.UserData == nil {
				it.UserData = &UserData{}
			}
			it.UserData.Watchlist = true
		}
	}
	return rows.Err()
}

// ---- collections ----

// Collection is a movie series (TMDB's "belongs_to_collection").
type Collection struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SetItemCollection records the collection a movie belongs to; nil clears it.
// Collections no movie belongs to any more are dropped.
func (s *Store) SetItemCollection(itemID int64, c *Collection) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c == nil || c.ID <= 0 || strings.TrimSpace(c.Name) == "" {
		if _, err := tx.Exec(`DELETE FROM item_collections WHERE item_id=?`, itemID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO collections(id,name) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name`, c.ID, strings.TrimSpace(c.Name)); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO item_collections(item_id,collection_id) VALUES(?,?)
			ON CONFLICT(item_id) DO UPDATE SET collection_id=excluded.collection_id`, itemID, c.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM collections WHERE NOT EXISTS (SELECT 1 FROM item_collections ic WHERE ic.collection_id=collections.id)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ItemCollection returns the collection an item belongs to, or ErrNotFound.
func (s *Store) ItemCollection(itemID int64) (*Collection, error) {
	c := &Collection{}
	err := s.db.QueryRow(`SELECT c.id,c.name FROM item_collections ic JOIN collections c ON c.id=ic.collection_id WHERE ic.item_id=?`, itemID).Scan(&c.ID, &c.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// CollectionItems returns a collection and its movies in the library, in
// release order (titles without a release date by year).
func (s *Store) CollectionItems(uid, collID int64) (*Collection, []*Item, error) {
	c := &Collection{}
	err := s.db.QueryRow(`SELECT id,name FROM collections WHERE id=?`, collID).Scan(&c.ID, &c.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	items, err := s.queryItemsUD(udSelect(`JOIN item_collections ic ON ic.item_id=i.id AND ic.collection_id=:cid
		ORDER BY CASE WHEN i.premiere<>'' THEN i.premiere WHEN i.year>0 THEN printf('%04d', i.year) ELSE '9999' END, i.sort_title`),
		sql.Named("uid", uid), sql.Named("cid", collID))
	return c, items, err
}

// ---- people ----

// PersonItems returns the movies and shows whose cast or crew lists someone
// by name (ignoring ASCII case), newest first.
func (s *Store) PersonItems(uid int64, name string) ([]*Item, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	return s.queryItemsUD(udSelect(`WHERE i.parent_id IS NULL AND i.kind IN ('movie','show')
		AND EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(i.cast_json) THEN i.cast_json ELSE '[]' END) c
			WHERE json_extract(c.value,'$.name')=:name COLLATE NOCASE)
		ORDER BY CASE WHEN i.premiere<>'' THEN i.premiere ELSE printf('%04d', i.year) END DESC, i.sort_title`),
		sql.Named("uid", uid), sql.Named("name", name))
}
