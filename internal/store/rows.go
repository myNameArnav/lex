package store

import (
	"database/sql"
	"fmt"
)

// RecentlyAdded returns top-level movies or recently added episodes.
func (s *Store) RecentlyAdded(uid int64, libID int64, kind string, limit int) ([]*Item, error) {
	cond := "i.kind=:kind"
	args := []any{sql.Named("uid", uid), sql.Named("kind", kind)}
	if libID > 0 {
		cond += " AND i.library_id=:lib"
		args = append(args, sql.Named("lib", libID))
	}
	return s.queryItemsUD(udSelect(fmt.Sprintf("WHERE %s ORDER BY i.added_at DESC, i.id DESC LIMIT %d", cond, limit)), args...)
}

// ContinueWatching returns items with a resume position, most recent first.
// A watched title with one is being rewatched: marking a title watched or
// unwatched, or finishing it, resets its position.
func (s *Store) ContinueWatching(uid int64, limit int) ([]*Item, error) {
	return s.queryItemsUD(udSelectCols(cardCols, fmt.Sprintf(`WHERE ud.position>0 AND i.kind IN ('movie','episode') ORDER BY ud.last_played DESC LIMIT %d`, limit)), sql.Named("uid", uid))
}

// NextUp returns, for each recently watched show, the first unwatched
// regular episode after the episode watched last, most recent show first.
// A show whose latest episode is only part watched is left to Continue
// Watching. Of the shows with the 2*limit most recent activity, at most
// limit are returned.
func (s *Store) NextUp(uid int64, limit int) ([]*Item, error) {
	return s.queryItemsUD(nextUpSQL, sql.Named("uid", uid), sql.Named("shows", limit*2), sql.Named("limit", limit))
}

// nextUpSQL: shows are the recently watched shows; last is each one's
// latest started or watched episode (ties go to the later one in episode
// order); nxt is the first unwatched non-special episode after it, if that
// one was watched.
var nextUpSQL = `WITH shows AS MATERIALIZED (
		SELECT i.show_id sid, MAX(ud.last_played) lp FROM user_data ud JOIN items i ON i.id=ud.item_id
		WHERE ud.user_id=:uid AND i.kind='episode' AND (ud.played=1 OR ud.position>0) AND ` + libAccess + `
		GROUP BY i.show_id ORDER BY lp DESC, i.show_id LIMIT :shows),
	last AS MATERIALIZED (
		SELECT sid, lp, (SELECT e.id FROM items e JOIN user_data u ON u.item_id=e.id AND u.user_id=:uid
			WHERE e.show_id=sid AND e.kind='episode' AND (u.played=1 OR u.position>0)
			ORDER BY u.last_played DESC, e.season DESC, e.episode DESC, e.id DESC LIMIT 1) lid
		FROM shows),
	nxt AS MATERIALIZED (
		SELECT l.sid, l.lp, (SELECT n.id FROM items n LEFT JOIN user_data u ON u.item_id=n.id AND u.user_id=:uid
			WHERE n.show_id=l.sid AND n.kind='episode' AND n.season<>0 AND COALESCE(u.played,0)=0
			AND (n.season,n.episode,n.id) > (le.season,le.episode,le.id)
			ORDER BY n.season, n.episode, n.id LIMIT 1) nid
		FROM last l CROSS JOIN items le ON le.id=l.lid CROSS JOIN user_data lu ON lu.item_id=le.id AND lu.user_id=:uid
		WHERE lu.played=1)
	` + udSelectCols(cardCols, `WHERE i.id IN (SELECT nid FROM nxt)
		ORDER BY (SELECT lp FROM nxt WHERE nxt.nid=i.id) DESC, i.show_id LIMIT :limit`)
