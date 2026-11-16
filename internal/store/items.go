package store

import (
	"database/sql"
	"errors"
	"fmt"
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

type Person struct {
	Name  string `json:"name"`
	Role  string `json:"role,omitempty"`
	Image string `json:"image,omitempty"`
}

// Hint carries what the scanner learned from names and sidecar files; it
// steers metadata lookups.
type Hint struct {
	Title string `json:"title,omitempty"`
	Year  int    `json:"year,omitempty"`
	TMDB  string `json:"tmdb,omitempty"`
	TVDB  string `json:"tvdb,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
}

type Item struct {
	ID            int64             `json:"id"`
	LibraryID     int64             `json:"libraryId"`
	Kind          string            `json:"kind"`
	ParentID      int64             `json:"parentId,omitempty"`
	ShowID        int64             `json:"showId,omitempty"`
	Title         string            `json:"title"`
	SortTitle     string            `json:"-"`
	OriginalTitle string            `json:"originalTitle,omitempty"`
	Year          int               `json:"year,omitempty"`
	Season        int               `json:"season"`
	Episode       int               `json:"episode,omitempty"`
	EpisodeEnd    int               `json:"episodeEnd,omitempty"`
	Overview      string            `json:"overview,omitempty"`
	Tagline       string            `json:"tagline,omitempty"`
	Rating        float64           `json:"rating,omitempty"`
	ContentRating string            `json:"contentRating,omitempty"`
	Genres        []string          `json:"genres,omitempty"`
	Cast          []Person          `json:"cast,omitempty"`
	Studios       []string          `json:"studios,omitempty"`
	Runtime       int               `json:"runtime,omitempty"`
	Premiere      string            `json:"premiere,omitempty"`
	Poster        string            `json:"-"`
	Backdrop      string            `json:"-"`
	Thumb         string            `json:"-"`
	ProviderIDs   map[string]string `json:"providerIds,omitempty"`
	Path          string            `json:"-"`
	AddedAt       int64             `json:"addedAt"`
	UpdatedAt     int64             `json:"updatedAt"`
	MetaStatus    int               `json:"metaStatus"`
	MetaLocked    bool              `json:"metaLocked,omitempty"`
	Hint          Hint              `json:"-"`

	// Populated by some queries.
	UserData      *UserData `json:"userData,omitempty"`
	ChildCount    int       `json:"childCount,omitempty"`
	UnplayedCount int       `json:"unplayedCount,omitempty"`
	ShowTitle     string    `json:"showTitle,omitempty"`
	Duration      float64   `json:"duration,omitempty"`
}

const (
	MetaPending  = 0
	MetaMatched  = 1
	MetaNotFound = 2
	MetaLocal    = 3
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

const itemCols = `i.id,i.library_id,i.kind,COALESCE(i.parent_id,0),i.show_id,i.title,i.sort_title,i.original_title,i.year,i.season,i.episode,i.episode_end,
	i.overview,i.tagline,i.rating,i.content_rating,i.genres,i.cast_json,i.studios,i.runtime,i.premiere,i.poster,i.backdrop,i.thumb,
	i.provider_ids,i.path,i.added_at,i.updated_at,i.meta_status,i.meta_locked,i.hint`

// cardCols is itemCols for list responses (grids, home rows, search): just
// what cards render. The columns cards don't use read as zero values in the
// same positions, so scanItem reads both, and the JSON leaves them out.
const cardCols = `i.id,i.library_id,i.kind,COALESCE(i.parent_id,0),i.show_id,i.title,i.sort_title,'',i.year,i.season,i.episode,i.episode_end,
	'','',0,'','','','',0,'','','','',
	'','',i.added_at,i.updated_at,i.meta_status,0,'{}'`

// childCols is cardCols plus what a show's season tabs and a season's
// episode rows show: the overview and the air date.
const childCols = `i.id,i.library_id,i.kind,COALESCE(i.parent_id,0),i.show_id,i.title,i.sort_title,'',i.year,i.season,i.episode,i.episode_end,
	i.overview,'',0,'','','','',0,i.premiere,'','','',
	'','',i.added_at,i.updated_at,i.meta_status,0,'{}'`

type scanner interface{ Scan(...any) error }

func scanItem(r scanner, extra ...any) (*Item, error) {
	it := &Item{}
	var genres, cast, studios, pids, hint string
	dest := []any{&it.ID, &it.LibraryID, &it.Kind, &it.ParentID, &it.ShowID, &it.Title, &it.SortTitle, &it.OriginalTitle, &it.Year, &it.Season, &it.Episode, &it.EpisodeEnd,
		&it.Overview, &it.Tagline, &it.Rating, &it.ContentRating, &genres, &cast, &studios, &it.Runtime, &it.Premiere, &it.Poster, &it.Backdrop, &it.Thumb,
		&pids, &it.Path, &it.AddedAt, &it.UpdatedAt, &it.MetaStatus, &it.MetaLocked, &hint}
	dest = append(dest, extra...)
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	jsonInto(genres, &it.Genres)
	jsonInto(cast, &it.Cast)
	jsonInto(studios, &it.Studios)
	jsonInto(pids, &it.ProviderIDs)
	jsonInto(hint, &it.Hint)
	return it, nil
}

func (s *Store) Item(id int64) (*Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM items i WHERE i.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// InsertItem creates an item and returns its id.
func (s *Store) InsertItem(it *Item) (int64, error) {
	if it.SortTitle == "" {
		it.SortTitle = SortTitle(it.Title)
	}
	if it.AddedAt == 0 {
		it.AddedAt = now()
	}
	it.UpdatedAt = now()
	res, err := s.db.Exec(`INSERT INTO items(library_id,kind,parent_id,show_id,title,sort_title,year,season,episode,episode_end,path,added_at,updated_at,meta_status,hint,premiere)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		it.LibraryID, it.Kind, nullID(it.ParentID), it.ShowID, it.Title, it.SortTitle, it.Year, it.Season, it.Episode, it.EpisodeEnd, it.Path, it.AddedAt, it.UpdatedAt, it.MetaStatus, jsonString(it.Hint), it.Premiere)
	if err != nil {
		return 0, err
	}
	it.ID, _ = res.LastInsertId()
	return it.ID, nil
}

// SaveMetadata writes every metadata column of an item.
func (s *Store) SaveMetadata(it *Item) error {
	_, err := s.saveMetadata(it, nil)
	return err
}

// MatchGuard pins the match an automatic metadata refresh started from: the
// lock and provider ids of the item that decides the match (the item itself,
// or the show for seasons and episodes).
type MatchGuard struct {
	ItemID      int64
	Locked      bool
	ProviderIDs string
}

// GuardOf captures an item's current match for SaveMetadataIf.
func GuardOf(it *Item) MatchGuard {
	return MatchGuard{ItemID: it.ID, Locked: it.MetaLocked, ProviderIDs: jsonString(nonNilM(it.ProviderIDs))}
}

// SaveMetadataIf is SaveMetadata for automatic refreshes, which work from a
// snapshot taken before slow network lookups: it only writes if the guarded
// item's match is unchanged, so a manual "Fix match" (or unmatch) made in the
// meantime wins. It reports whether the item was written.
func (s *Store) SaveMetadataIf(it *Item, g MatchGuard) (bool, error) {
	return s.saveMetadata(it, &g)
}

func (s *Store) saveMetadata(it *Item, g *MatchGuard) (bool, error) {
	if it.SortTitle == "" {
		it.SortTitle = SortTitle(it.Title)
	}
	it.UpdatedAt = now()
	q := `UPDATE items SET title=?,sort_title=?,original_title=?,year=?,overview=?,tagline=?,rating=?,content_rating=?,
		genres=?,cast_json=?,studios=?,runtime=?,premiere=?,poster=?,backdrop=?,thumb=?,provider_ids=?,updated_at=?,meta_status=?,meta_locked=?,hint=? WHERE id=?`
	args := []any{it.Title, it.SortTitle, it.OriginalTitle, it.Year, it.Overview, it.Tagline, it.Rating, it.ContentRating,
		jsonString(nonNil(it.Genres)), jsonString(nonNilP(it.Cast)), jsonString(nonNil(it.Studios)), it.Runtime, it.Premiere, it.Poster, it.Backdrop, it.Thumb,
		jsonString(nonNilM(it.ProviderIDs)), it.UpdatedAt, it.MetaStatus, it.MetaLocked, jsonString(it.Hint), it.ID}
	if g != nil {
		q += ` AND EXISTS (SELECT 1 FROM items g WHERE g.id=? AND g.meta_locked=? AND (CASE WHEN g.provider_ids IN ('','null') THEN '{}' ELSE g.provider_ids END)=?)`
		args = append(args, g.ItemID, g.Locked, g.ProviderIDs)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
func nonNilP(v []Person) []Person {
	if v == nil {
		return []Person{}
	}
	return v
}
func nonNilM(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}

func (s *Store) SetItemStructure(id int64, title string, year, season, episode, episodeEnd int, hint Hint) error {
	_, err := s.db.Exec(`UPDATE items SET year=CASE WHEN meta_status=1 THEN year ELSE ? END, season=?,episode=?,episode_end=?,hint=?,
		title=CASE WHEN meta_status=1 THEN title ELSE ? END, sort_title=CASE WHEN meta_status=1 THEN sort_title ELSE ? END WHERE id=?`,
		year, season, episode, episodeEnd, jsonString(hint), title, SortTitle(title), id)
	return err
}

func (s *Store) ResetMetadata(id int64) error {
	_, err := s.db.Exec(`UPDATE items SET meta_status=0 WHERE id=? OR parent_id=? OR show_id=?`, id, id, id)
	return err
}

func (s *Store) SetMetaStatus(id int64, status int) error {
	_, err := s.db.Exec(`UPDATE items SET meta_status=? WHERE id=?`, status, id)
	return err
}

// FindChild returns the child of parent with the given kind and key.
func (s *Store) FindItemByPath(libID int64, kind, path string) (*Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM items i WHERE i.library_id=? AND i.kind=? AND i.path=?`, libID, kind, path))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

// SeasonKey is the path key of a show's season item.
func SeasonKey(showKey string, season int) string {
	return fmt.Sprintf("%s|S%d", showKey, season)
}

// PlaceEpisode moves an episode to another season and number of its show,
// making the season if the show doesn't have it, and dropping the season
// it leaves if that is now empty. Date-named episodes of daily shows are
// grouped by year until their provider's episode list places them. It
// reports false, changing nothing, if the show already has an episode at
// that place.
func (s *Store) PlaceEpisode(epID int64, season, episode int) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var libID, showID, oldSeason int64
	var showPath string
	err = tx.QueryRow(`SELECT e.library_id, e.show_id, COALESCE(e.parent_id,0), sh.path FROM items e JOIN items sh ON sh.id=e.show_id
		WHERE e.id=? AND e.kind='episode'`, epID).Scan(&libID, &showID, &oldSeason, &showPath)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	} else if err != nil {
		return false, err
	}
	var taken int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM items WHERE show_id=? AND kind='episode' AND season=? AND episode=? AND id<>?`,
		showID, season, episode, epID).Scan(&taken); err != nil || taken > 0 {
		return false, err
	}
	var seasonID int64
	err = tx.QueryRow(`SELECT id FROM items WHERE parent_id=? AND kind='season' AND season=?`, showID, season).Scan(&seasonID)
	if errors.Is(err, sql.ErrNoRows) {
		title := fmt.Sprintf("Season %d", season)
		if season == 0 {
			title = "Specials"
		}
		t := now()
		res, err := tx.Exec(`INSERT INTO items(library_id,kind,parent_id,show_id,title,sort_title,season,path,added_at,updated_at,meta_status)
			VALUES(?,'season',?,?,?,?,?,?,?,?,?)`, libID, showID, showID, title, SortTitle(title), season, SeasonKey(showPath, season), t, t, MetaPending)
		if err != nil {
			return false, err
		}
		seasonID, _ = res.LastInsertId()
	} else if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE items SET parent_id=?, season=?, episode=?, episode_end=0 WHERE id=?`, seasonID, season, episode, epID); err != nil {
		return false, err
	}
	if oldSeason != seasonID {
		if _, err := tx.Exec(`DELETE FROM items WHERE id=? AND kind='season' AND NOT EXISTS (SELECT 1 FROM items e WHERE e.parent_id=?)`, oldSeason, oldSeason); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *Store) FindSeason(showID int64, season int) (*Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM items i WHERE i.parent_id=? AND i.kind='season' AND i.season=?`, showID, season))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return it, err
}

// ItemsByKind returns items of a kind for a library (or all libraries if libID==0).
func (s *Store) ItemsByKind(libID int64, kind string) ([]*Item, error) {
	q := `SELECT ` + itemCols + ` FROM items i WHERE i.kind=?`
	args := []any{kind}
	if libID > 0 {
		q += ` AND i.library_id=?`
		args = append(args, libID)
	}
	return s.queryItems(q, args...)
}

func (s *Store) ItemsNeedingMetadata() ([]*Item, error) {
	// Parents first so children can reuse the parent's provider ids.
	return s.queryItems(`SELECT ` + itemCols + ` FROM items i WHERE i.meta_status=0 ORDER BY CASE i.kind WHEN 'show' THEN 0 WHEN 'movie' THEN 1 WHEN 'season' THEN 2 ELSE 3 END, i.id`)
}

func (s *Store) queryItems(q string, args ...any) ([]*Item, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// queryItemsUD is like queryItems but expects the user_data columns and
// aggregate columns appended to each row.
func (s *Store) queryItemsUD(q string, args ...any) ([]*Item, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		ud := &UserData{}
		var childCount, unplayed int
		var showTitle string
		var duration float64
		it, err := scanItem(rows, &ud.Position, &ud.Played, &ud.PlayCount, &ud.LastPlayed, &ud.Favorite, &childCount, &unplayed, &showTitle, &duration)
		if err != nil {
			return nil, err
		}
		it.UserData = ud
		it.ChildCount = childCount
		it.UnplayedCount = unplayed
		it.ShowTitle = showTitle
		it.Duration = duration
		out = append(out, it)
	}
	return out, rows.Err()
}

// libAccess limits items i to the libraries user :uid may see: all of them
// for an admin or an unrestricted user, else those in user_libraries. A uid
// of 0 is the server itself (scans, metadata, the cache), which sees all.
const libAccess = `(:uid=0 OR EXISTS(SELECT 1 FROM users au WHERE au.id=:uid AND (au.is_admin=1 OR au.all_libraries=1))
	OR i.library_id IN (SELECT ul.library_id FROM user_libraries ul WHERE ul.user_id=:uid))`

// udSelect builds a select of items joined with a user's data plus
// aggregates. where starts with "WHERE"; only items in libraries the user
// may see are returned (libAccess).
func udSelect(where string) string { return udSelectCols(itemCols, where) }

// udSelectCols is udSelect with another projection (cardCols, childCols).
func udSelectCols(cols, where string) string {
	cond, ok := strings.CutPrefix(where, "WHERE ")
	if !ok {
		panic("udSelect: where must start with WHERE")
	}
	return `SELECT ` + cols + `,
		COALESCE(ud.position,0),COALESCE(ud.played,0),COALESCE(ud.play_count,0),COALESCE(ud.last_played,0),COALESCE(ud.favorite,0),
		CASE i.kind WHEN 'show' THEN (SELECT COUNT(*) FROM items e WHERE e.show_id=i.id AND e.kind='episode')
		            WHEN 'season' THEN (SELECT COUNT(*) FROM items e WHERE e.parent_id=i.id AND e.kind='episode') ELSE 0 END,
		CASE i.kind WHEN 'show' THEN (SELECT COUNT(*) FROM items e LEFT JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.show_id=i.id AND e.kind='episode' AND COALESCE(u2.played,0)=0)
		            WHEN 'season' THEN (SELECT COUNT(*) FROM items e LEFT JOIN user_data u2 ON u2.item_id=e.id AND u2.user_id=:uid WHERE e.parent_id=i.id AND e.kind='episode' AND COALESCE(u2.played,0)=0) ELSE 0 END,
		COALESCE((SELECT s.title FROM items s WHERE s.id=i.show_id),''),
		COALESCE((SELECT MAX(f.duration) FROM files f WHERE f.item_id=i.id),0)
		FROM items i LEFT JOIN user_data ud ON ud.item_id=i.id AND ud.user_id=:uid WHERE ` + libAccess + ` AND ` + cond
}

func (s *Store) ItemForUser(id, uid int64) (*Item, error) {
	items, err := s.queryItemsUD(udSelect(`WHERE i.id=:id`), sql.Named("uid", uid), sql.Named("id", id))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items[0], nil
}

func (s *Store) Children(parentID, uid int64) ([]*Item, error) {
	return s.children(itemCols, parentID, uid)
}

// ChildCards is Children with only what season tabs and episode rows show
// (childCols).
func (s *Store) ChildCards(parentID, uid int64) ([]*Item, error) {
	return s.children(childCols, parentID, uid)
}

func (s *Store) children(cols string, parentID, uid int64) ([]*Item, error) {
	return s.queryItemsUD(udSelectCols(cols, `WHERE i.parent_id=:pid ORDER BY i.season, i.episode, i.sort_title`), sql.Named("uid", uid), sql.Named("pid", parentID))
}

// episodeOrder is a show's episode order: by season and episode, then id
// so that duplicates keep a stable order. It matches the items_show_order index.
const episodeOrder = `i.season, i.episode, i.id`

func (s *Store) ShowEpisodes(showID, uid int64) ([]*Item, error) {
	return s.queryItemsUD(udSelect(`WHERE i.show_id=:sid AND i.kind='episode' ORDER BY `+episodeOrder), sql.Named("uid", uid), sql.Named("sid", showID))
}

// oneItemUD runs a udSelect for at most one item, or nil.
func (s *Store) oneItemUD(q string, args ...any) (*Item, error) {
	items, err := s.queryItemsUD(q, args...)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return items[0], nil
}

// AdjacentEpisodes returns the episodes before and after ep in its show's
// episode order (specials included), or nil at either end.
func (s *Store) AdjacentEpisodes(ep *Item, uid int64) (prev, next *Item, err error) {
	// The id is found first, so the select's aggregates run for one row.
	near := func(cmp, dir string) (*Item, error) {
		return s.oneItemUD(udSelect(`WHERE i.id=(SELECT e.id FROM items e WHERE e.show_id=:sid AND e.kind='episode'
			AND (e.season,e.episode,e.id) `+cmp+` (:season,:episode,:id)
			ORDER BY e.season `+dir+`, e.episode `+dir+`, e.id `+dir+` LIMIT 1)`),
			sql.Named("uid", uid), sql.Named("sid", ep.ShowID), sql.Named("season", ep.Season), sql.Named("episode", ep.Episode), sql.Named("id", ep.ID))
	}
	if prev, err = near("<", "DESC"); err != nil {
		return nil, nil, err
	}
	next, err = near(">", "ASC")
	return prev, next, err
}

// ShowNextEpisode is what "Play" on a show starts: its first unwatched
// regular (non-special) episode, or else its first episode; nil if it has
// none.
func (s *Store) ShowNextEpisode(showID, uid int64) (*Item, error) {
	return s.oneItemUD(udSelect(`WHERE i.id=(SELECT e.id FROM items e LEFT JOIN user_data u ON u.item_id=e.id AND u.user_id=:uid
		WHERE e.show_id=:sid AND e.kind='episode'
		ORDER BY (e.season>0 AND COALESCE(u.played,0)=0) DESC, e.season, e.episode, e.id LIMIT 1)`),
		sql.Named("uid", uid), sql.Named("sid", showID))
}

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

// ListItems returns a page of titles as cards (cardCols) and the total.
func (s *Store) ListItems(uid int64, q ListQuery) ([]*Item, int, error) {
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
func (s *Store) ContinueWatching(uid int64, limit int) ([]*Item, error) {
	return s.queryItemsUD(udSelectCols(cardCols, fmt.Sprintf(`WHERE ud.position>0 AND ud.played=0 AND i.kind IN ('movie','episode') ORDER BY ud.last_played DESC LIMIT %d`, limit)), sql.Named("uid", uid))
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

// PruneEmpty deletes seasons and shows that no longer have episodes, and
// movies/episodes without files.
func (s *Store) PruneEmpty(libID int64) (int64, error) {
	var total int64
	stmts := []string{
		`DELETE FROM items WHERE library_id=? AND kind IN ('movie','episode') AND NOT EXISTS (SELECT 1 FROM files f WHERE f.item_id=items.id)`,
		`DELETE FROM items WHERE library_id=? AND kind='season' AND NOT EXISTS (SELECT 1 FROM items e WHERE e.parent_id=items.id)`,
		`DELETE FROM items WHERE library_id=? AND kind='show' AND NOT EXISTS (SELECT 1 FROM items e WHERE e.parent_id=items.id)`,
	}
	for _, q := range stmts {
		res, err := s.db.Exec(q, libID)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// ---- user data ----

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
	res, err := s.db.Exec(`INSERT INTO user_data(user_id,item_id,favorite) SELECT ?,id,? FROM items WHERE id=?
		ON CONFLICT(user_id,item_id) DO UPDATE SET favorite=excluded.favorite`, uid, fav, itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

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
			(SELECT f.duration FROM files f WHERE f.item_id=i.id ORDER BY f.height DESC, f.size DESC LIMIT 1) dur
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

// ItemsForUser is ItemForUser for several items at once, keyed by id;
// missing ids are left out.
func (s *Store) ItemsForUser(ids []int64, uid int64) (map[int64]*Item, error) {
	out := map[int64]*Item{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{sql.Named("uid", uid)}
	for _, id := range ids {
		args = append(args, id)
	}
	items, err := s.queryItemsUD(udSelect(`WHERE i.id IN (`+ph+`)`), args...)
	for _, it := range items {
		out[it.ID] = it
	}
	return out, err
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
