package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// The search index: an FTS5 table of every item's titles, cast names and
// genres, keyed by item id and kept in step with items by triggers. Words
// are folded for case and accents (unicode61) and matched by prefix.

// searchIndexVersion names the index's shape: a database built with
// another one is re-indexed on open.
const searchIndexVersion = "1"

// ftsRow is the index row of the item the alias names: cast names and
// genres come out of their JSON, skipping anything that isn't valid.
func ftsRow(a string) string {
	return fmt.Sprintf(`%[1]s.id, %[1]s.title, %[1]s.original_title,
		COALESCE((SELECT group_concat(CASE WHEN j.type='object' THEN json_extract(j.value,'$.name') END,' ')
			FROM json_each(CASE WHEN json_valid(%[1]s.cast_json) THEN %[1]s.cast_json ELSE '[]' END) j),''),
		COALESCE((SELECT group_concat(CASE WHEN j.type='text' THEN j.value END,' ')
			FROM json_each(CASE WHEN json_valid(%[1]s.genres) THEN %[1]s.genres ELSE '[]' END) j),'')`, a)
}

var searchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(title, original_title, people, genres,
	tokenize='unicode61 remove_diacritics 2', prefix='2 3');
CREATE TRIGGER IF NOT EXISTS items_fts_insert AFTER INSERT ON items BEGIN
	INSERT INTO items_fts(rowid,title,original_title,people,genres) SELECT ` + ftsRow("new") + `;
END;
CREATE TRIGGER IF NOT EXISTS items_fts_delete AFTER DELETE ON items BEGIN
	DELETE FROM items_fts WHERE rowid=old.id;
END;
CREATE TRIGGER IF NOT EXISTS items_fts_update AFTER UPDATE OF title,original_title,cast_json,genres ON items BEGIN
	DELETE FROM items_fts WHERE rowid=old.id;
	INSERT INTO items_fts(rowid,title,original_title,people,genres) SELECT ` + ftsRow("new") + `;
END;
`

// migrateSearch creates the search index and fills it when it is new, of
// an older version, or out of step with items.
func migrateSearch(db *sql.DB) error {
	if _, err := db.Exec(searchSchema); err != nil {
		return err
	}
	var version string
	var items, indexed int
	if err := db.QueryRow(`SELECT COALESCE((SELECT value FROM kv WHERE key='search_index'),''),
		(SELECT COUNT(*) FROM items), (SELECT COUNT(*) FROM items_fts)`).Scan(&version, &items, &indexed); err != nil {
		return err
	}
	if version == searchIndexVersion && items == indexed {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM items_fts`,
		`INSERT INTO items_fts(rowid,title,original_title,people,genres) SELECT ` + ftsRow("items") + ` FROM items`,
		`INSERT INTO kv(key,value) VALUES('search_index','` + searchIndexVersion + `') ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const (
	// ftsCond limits a list to the items the search index matches (:fts).
	ftsCond = `i.id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH :fts)`
	// ftsTitleFirst orders items whose titles match (:ftstitle) first.
	ftsTitleFirst = `CASE WHEN i.id IN (SELECT rowid FROM items_fts WHERE items_fts MATCH :ftstitle) THEN 0 ELSE 1 END`
	// likeCond matches the title or original title against :q, literally:
	// % and _ in the term are not wildcards. It reads the titles from the
	// index, which is far smaller to scan than items.
	likeCond = `i.id IN (SELECT rowid FROM items_fts WHERE title LIKE :q ESCAPE '\' OR original_title LIKE :q ESCAPE '\')`
)

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func likePattern(term string) string { return "%" + likeEscaper.Replace(term) + "%" }

// ftsQuery turns a search term into an FTS5 query: every word must start a
// word of the item, in any order and any indexed column. A word is quoted,
// so it is never query syntax; punctuation inside one ("spider-man") makes
// it a phrase. Words with nothing to index are dropped, and a term without
// any gives "" (search by substring instead).
func ftsQuery(term string) string {
	var parts []string
	for _, w := range strings.Fields(term) {
		if !strings.ContainsFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(w, `"`, `""`)+`"*`)
	}
	return strings.Join(parts, " ")
}

// ftsTitleQuery limits an ftsQuery to the title columns.
func ftsTitleQuery(q string) string { return "{title original_title} : (" + q + ")" }
