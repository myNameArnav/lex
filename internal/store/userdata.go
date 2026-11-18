package store

import (
	"database/sql"
	"errors"
)

type UserData struct {
	Position   float64 `json:"position"`
	Played     bool    `json:"played"`
	PlayCount  int     `json:"playCount"`
	LastPlayed int64   `json:"lastPlayed"`
	Favorite   bool    `json:"favorite"`
	// Watchlist is set by Watchlist and AnnotateWatchlist only.
	Watchlist bool `json:"watchlist,omitempty"`
}

// ---- libraries ----

func (s *Store) GetUserData(uid, itemID int64) (UserData, error) {
	var ud UserData
	err := s.db.QueryRow(`SELECT position,played,play_count,last_played,favorite FROM user_data WHERE user_id=? AND item_id=?`, uid, itemID).
		Scan(&ud.Position, &ud.Played, &ud.PlayCount, &ud.LastPlayed, &ud.Favorite)
	if errors.Is(err, sql.ErrNoRows) {
		return ud, nil
	}
	return ud, err
}

func (s *Store) SaveProgress(uid, itemID int64, position float64) error {
	_, err := s.db.Exec(`INSERT INTO user_data(user_id,item_id,position,last_played) VALUES(?,?,?,?)
		ON CONFLICT(user_id,item_id) DO UPDATE SET position=excluded.position,last_played=excluded.last_played`, uid, itemID, position, now())
	return err
}

// ClearProgress drops a title's resume point (Remove from Continue
// Watching), leaving whether it was watched alone.
func (s *Store) ClearProgress(uid, itemID int64) error {
	_, err := s.db.Exec(`UPDATE user_data SET position=0 WHERE user_id=? AND item_id=?`, uid, itemID)
	return err
}

func (s *Store) MarkPlayed(uid, itemID int64, played bool) error {
	if played {
		_, err := s.db.Exec(`INSERT INTO user_data(user_id,item_id,played,play_count,position,last_played) VALUES(?,?,1,1,0,?)
			ON CONFLICT(user_id,item_id) DO UPDATE SET played=1,play_count=play_count+1,position=0,last_played=excluded.last_played`, uid, itemID, now())
		return err
	}
	_, err := s.db.Exec(`UPDATE user_data SET played=0,position=0 WHERE user_id=? AND item_id=?`, uid, itemID)
	return err
}

// UndoPlayed reverses a MarkPlayed made moments ago by a playback session
// whose viewer then went back: the play is uncounted, the watched state
// restored and the resume point set to where the viewer is now.
func (s *Store) UndoPlayed(uid, itemID int64, wasPlayed bool, position float64) error {
	_, err := s.db.Exec(`UPDATE user_data SET played=?,play_count=MAX(play_count-1,0),position=? WHERE user_id=? AND item_id=?`,
		wasPlayed, position, uid, itemID)
	return err
}

// MarkTreePlayed is the manual "Mark watched/unwatched" for a show or season
// (all its episodes) or a single item. Unlike MarkPlayed, which records a
// finished playback, marking something that is already watched doesn't add
// a play or move it in Next Up. It's one statement, so a show is never left
// half-marked.
func (s *Store) MarkTreePlayed(uid, itemID int64, played bool) error {
	it, err := s.Item(itemID)
	if err != nil {
		return err
	}
	var cond string
	switch it.Kind {
	case "show":
		cond = `e.show_id=:id AND e.kind='episode'`
	case "season":
		cond = `e.parent_id=:id AND e.kind='episode'`
	default:
		cond = `e.id=:id`
	}
	args := []any{sql.Named("uid", uid), sql.Named("id", itemID), sql.Named("now", now())}
	if !played {
		_, err = s.db.Exec(`UPDATE user_data SET played=0,position=0 WHERE user_id=:uid AND item_id IN (SELECT e.id FROM items e WHERE `+cond+`)`, args...)
		return err
	}
	// "WHERE true": an upsert's SELECT needs a WHERE clause to parse.
	_, err = s.db.Exec(`INSERT INTO user_data(user_id,item_id,played,play_count,position,last_played)
		SELECT :uid, e.id, 1, 1, 0, :now FROM items e WHERE `+cond+` AND true
		ON CONFLICT(user_id,item_id) DO UPDATE SET played=1, position=0,
			play_count=play_count+CASE WHEN played=1 THEN 0 ELSE 1 END,
			last_played=CASE WHEN played=1 THEN last_played ELSE excluded.last_played END`, args...)
	return err
}

// SetFavorite returns ErrNotFound for an item that doesn't exist.
func (s *Store) SetFavorite(uid, itemID int64, fav bool) error {
	// favorited_at keeps the time it first became a favorite while it stays one.
	res, err := s.db.Exec(`INSERT INTO user_data(user_id,item_id,favorite,favorited_at) SELECT ?,id,?,? FROM items WHERE id=?
		ON CONFLICT(user_id,item_id) DO UPDATE SET favorite=excluded.favorite,
			favorited_at=CASE WHEN excluded.favorite=0 THEN 0 WHEN user_data.favorite=1 THEN user_data.favorited_at ELSE excluded.favorited_at END`,
		uid, fav, favoritedAt(fav), itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func favoritedAt(fav bool) int64 {
	if fav {
		return now()
	}
	return 0
}

// Favorites returns the movies and shows a user marked as favorites, across
// the libraries they may see, most recently added first; limit <= 0 returns
// all of them.
func (s *Store) Favorites(uid int64, limit int) ([]*Item, error) {
	q := `WHERE i.kind IN ('movie','show') AND i.id IN (SELECT item_id FROM user_data WHERE user_id=:uid AND favorite=1)
		ORDER BY ud.favorited_at DESC, i.id DESC`
	args := []any{sql.Named("uid", uid)}
	if limit > 0 {
		q += ` LIMIT :lim`
		args = append(args, sql.Named("lim", limit))
	}
	// Cards only, like ListItems: the Home row and page show posters.
	return s.queryItemsUD(udSelectCols(cardCols, q), args...)
}
