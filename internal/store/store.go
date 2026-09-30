// Package store owns the SQLite database: schema, migrations and typed queries.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
	// cfgMu guards the cached config document.
	cfgMu sync.RWMutex
	cfg   Config
}

const schema = `
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	pass_hash TEXT NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	prefs TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS tokens (
	token TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	last_seen INTEGER NOT NULL,
	client TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS libraries (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	paths TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	last_scan INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS items (
	id INTEGER PRIMARY KEY,
	library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	parent_id INTEGER REFERENCES items(id) ON DELETE CASCADE,
	show_id INTEGER NOT NULL DEFAULT 0,
	title TEXT NOT NULL,
	sort_title TEXT NOT NULL,
	original_title TEXT NOT NULL DEFAULT '',
	year INTEGER NOT NULL DEFAULT 0,
	season INTEGER NOT NULL DEFAULT 0,
	episode INTEGER NOT NULL DEFAULT 0,
	episode_end INTEGER NOT NULL DEFAULT 0,
	overview TEXT NOT NULL DEFAULT '',
	tagline TEXT NOT NULL DEFAULT '',
	rating REAL NOT NULL DEFAULT 0,
	content_rating TEXT NOT NULL DEFAULT '',
	genres TEXT NOT NULL DEFAULT '[]',
	cast_json TEXT NOT NULL DEFAULT '[]',
	studios TEXT NOT NULL DEFAULT '[]',
	runtime INTEGER NOT NULL DEFAULT 0,
	premiere TEXT NOT NULL DEFAULT '',
	poster TEXT NOT NULL DEFAULT '',
	backdrop TEXT NOT NULL DEFAULT '',
	thumb TEXT NOT NULL DEFAULT '',
	provider_ids TEXT NOT NULL DEFAULT '{}',
	path TEXT NOT NULL DEFAULT '',
	added_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	meta_status INTEGER NOT NULL DEFAULT 0,
	meta_locked INTEGER NOT NULL DEFAULT 0,
	hint TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS items_parent ON items(parent_id);
CREATE INDEX IF NOT EXISTS items_show ON items(show_id, kind);
CREATE INDEX IF NOT EXISTS items_lib_kind ON items(library_id, kind);
CREATE INDEX IF NOT EXISTS items_path ON items(library_id, kind, path);
CREATE INDEX IF NOT EXISTS items_added ON items(added_at);

CREATE TABLE IF NOT EXISTS files (
	id INTEGER PRIMARY KEY,
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
	path TEXT NOT NULL UNIQUE,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	container TEXT NOT NULL DEFAULT '',
	duration REAL NOT NULL DEFAULT 0,
	bitrate INTEGER NOT NULL DEFAULT 0,
	width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0,
	vcodec TEXT NOT NULL DEFAULT '',
	acodec TEXT NOT NULL DEFAULT '',
	hdr TEXT NOT NULL DEFAULT '',
	info TEXT NOT NULL DEFAULT '',
	probed_at INTEGER NOT NULL DEFAULT 0,
	probe_error TEXT NOT NULL DEFAULT '',
	added_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS files_item ON files(item_id);

CREATE TABLE IF NOT EXISTS user_data (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	position REAL NOT NULL DEFAULT 0,
	played INTEGER NOT NULL DEFAULT 0,
	play_count INTEGER NOT NULL DEFAULT 0,
	last_played INTEGER NOT NULL DEFAULT 0,
	favorite INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, item_id)
);
CREATE INDEX IF NOT EXISTS user_data_recent ON user_data(user_id, last_played);

CREATE TABLE IF NOT EXISTS history (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL,
	user_name TEXT NOT NULL DEFAULT '',
	item_id INTEGER NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	file_id INTEGER NOT NULL DEFAULT 0,
	started_at INTEGER NOT NULL,
	ended_at INTEGER NOT NULL,
	watched REAL NOT NULL DEFAULT 0,
	method TEXT NOT NULL DEFAULT '',
	reasons TEXT NOT NULL DEFAULT '',
	client TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	remote INTEGER NOT NULL DEFAULT 0,
	bytes INTEGER NOT NULL DEFAULT 0,
	video_out TEXT NOT NULL DEFAULT '',
	audio_out TEXT NOT NULL DEFAULT '',
	buffer_events INTEGER NOT NULL DEFAULT 0,
	buffer_seconds REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS history_started ON history(started_at);

CREATE TABLE IF NOT EXISTS cache_entries (
	file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	src_mtime INTEGER NOT NULL,
	added_at INTEGER NOT NULL,
	last_access INTEGER NOT NULL,
	hits INTEGER NOT NULL DEFAULT 0,
	reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS segments (
	item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	start REAL NOT NULL,
	end REAL NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (item_id, kind)
);

CREATE TABLE IF NOT EXISTS intro_scans (
	file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
	scanned_at INTEGER NOT NULL,
	result TEXT NOT NULL DEFAULT ''
);
`

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "lex.db")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath}).String() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-4000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite serialises writers anyway; a small pool keeps memory low.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: db}
	if err := s.loadConfig(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

func now() int64 { return time.Now().Unix() }

func (s *Store) getKV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) setKV(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- JSON helpers ----

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonInto(s string, v any) {
	if s == "" {
		return
	}
	_ = json.Unmarshal([]byte(s), v)
}

// Tx runs fn inside a transaction.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// SortTitle strips leading articles for natural sorting.
func SortTitle(t string) string {
	l := strings.ToLower(strings.TrimSpace(t))
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(l, a) && len(l) > len(a) {
			return strings.TrimSpace(l[len(a):])
		}
	}
	return l
}
