package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

var ErrSetupCompleted = errors.New("setup already completed")

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must be between 12 and 72 bytes")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

type User struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsAdmin   bool   `json:"isAdmin"`
	CreatedAt int64  `json:"createdAt"`
	Prefs     string `json:"-"`
}

func (s *Store) UserCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(name, password string, admin bool) (*User, error) {
	return s.createUser(name, password, admin, false)
}

// CreateInitialAdmin uses a single conditional write so simultaneous setup
// requests cannot create multiple administrators.
func (s *Store) CreateInitialAdmin(name, password string) (*User, error) {
	return s.createUser(name, password, true, true)
}

func (s *Store) createUser(name, password string, admin, initial bool) (*User, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, errors.New("name must be between 1 and 64 bytes")
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return nil, errors.New("name must not contain control characters")
	}
	h, err := passwordHash(password)
	if err != nil {
		return nil, err
	}
	query := `INSERT INTO users(name,pass_hash,is_admin,created_at) VALUES(?,?,?,?)`
	if initial {
		query = `INSERT INTO users(name,pass_hash,is_admin,created_at) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM users)`
	}
	res, err := s.db.Exec(query, name, h, admin, now())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, errors.New("a user with that name already exists")
		}
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, ErrSetupCompleted
	}
	id, _ := res.LastInsertId()
	return s.User(id)
}

func (s *Store) User(id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,is_admin,created_at,prefs FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.Prefs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) Users() ([]User, error) {
	rows, err := s.db.Query(`SELECT id,name,is_admin,created_at,prefs FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.IsAdmin, &u.CreatedAt, &u.Prefs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// Authenticate checks credentials. It always runs bcrypt to keep timing uniform.
func (s *Store) Authenticate(name, password string) (*User, error) {
	var id int64
	var hash string
	err := s.db.QueryRow(`SELECT id,pass_hash FROM users WHERE name=?`, strings.TrimSpace(name)).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		dummyOnce.Do(func() {
			h, _ := bcrypt.GenerateFromPassword([]byte("dummy-password"), bcrypt.DefaultCost)
			dummyHash = string(h)
		})
		hash = dummyHash
		id = 0
	} else if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || id == 0 {
		return nil, errors.New("invalid username or password")
	}
	return s.User(id)
}

func (s *Store) SetPassword(id int64, password string) error {
	h, err := passwordHash(password)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET pass_hash=? WHERE id=?`, h, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM tokens WHERE user_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAdmin(id int64, admin bool) error {
	res, err := s.db.Exec(`UPDATE users SET is_admin=? WHERE id=? AND (? OR is_admin=0 OR (SELECT COUNT(*) FROM users WHERE is_admin=1)>1)`, admin, id, admin)
	return s.checkAdminChange(id, res, err)
}

func (s *Store) SetPrefs(id int64, prefs string) error {
	_, err := s.db.Exec(`UPDATE users SET prefs=? WHERE id=?`, prefs, id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id=? AND (is_admin=0 OR (SELECT COUNT(*) FROM users WHERE is_admin=1)>1)`, id)
	return s.checkAdminChange(id, res, err)
}

func (s *Store) checkAdminChange(id int64, res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return err
	}
	if _, err := s.User(id); err != nil {
		return err
	}
	return errors.New("can't remove the last admin")
}

func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin=1`).Scan(&n)
	return n, err
}

// ---- tokens ----

// Session tokens are bearer secrets, so only their SHA-256 is stored: a
// leaked database or backup can't be replayed as a session. The raw token is
// handed to the client once, by CreateToken. Devices are named by the first
// DeviceIDLen hex characters of the hash, which reveal nothing usable.

// DeviceIDLen is the length of a device id (a prefix of the token hash).
const DeviceIDLen = 16

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// DeviceID is the id the devices list shows for a session token.
func DeviceID(token string) string { return hashToken(token)[:DeviceIDLen] }

func (s *Store) CreateToken(userID int64, client, ip string) (string, error) {
	t := RandomToken(24)
	_, err := s.db.Exec(`INSERT INTO tokens(token,user_id,created_at,last_seen,client,ip) VALUES(?,?,?,?,?,?)`,
		hashToken(t), userID, now(), now(), client, ip)
	return t, err
}

// TokenUser resolves a token. last_seen is refreshed at most every 10 minutes
// to avoid a write on every request.
func (s *Store) TokenUser(token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	h := hashToken(token)
	var uid, seen int64
	err := s.db.QueryRow(`SELECT user_id,last_seen FROM tokens WHERE token=?`, h).Scan(&uid, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Tokens expire after 90 days of inactivity.
	if now()-seen > 90*86400 {
		s.db.Exec(`DELETE FROM tokens WHERE token=?`, h)
		return nil, ErrNotFound
	}
	if now()-seen > 600 {
		s.db.Exec(`UPDATE tokens SET last_seen=? WHERE token=?`, now(), h)
	}
	return s.User(uid)
}

func (s *Store) DeleteToken(token string) error {
	_, err := s.db.Exec(`DELETE FROM tokens WHERE token=?`, hashToken(token))
	return err
}

type TokenInfo struct {
	Prefix   string `json:"prefix"` // device id: a prefix of the token hash, not of the token
	UserID   int64  `json:"userId"`
	UserName string `json:"userName"`
	Created  int64  `json:"created"`
	LastSeen int64  `json:"lastSeen"`
	Client   string `json:"client"`
	IP       string `json:"ip"`
	Current  bool   `json:"current"` // the requesting session (set by the API)
}

func (s *Store) Tokens() ([]TokenInfo, error) {
	rows, err := s.db.Query(`SELECT substr(t.token,1,?),t.user_id,u.name,t.created_at,t.last_seen,t.client,t.ip FROM tokens t JOIN users u ON u.id=t.user_id ORDER BY t.last_seen DESC`, DeviceIDLen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenInfo
	for rows.Next() {
		var t TokenInfo
		if err := rows.Scan(&t.Prefix, &t.UserID, &t.UserName, &t.Created, &t.LastSeen, &t.Client, &t.IP); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteTokenPrefix signs out the device with the given id (see DeviceID).
func (s *Store) DeleteTokenPrefix(prefix string) error {
	if len(prefix) != DeviceIDLen || strings.Trim(prefix, "0123456789abcdef") != "" {
		return errors.New("bad device id")
	}
	_, err := s.db.Exec(`DELETE FROM tokens WHERE substr(token,1,?)=?`, DeviceIDLen, prefix)
	return err
}

// migrateTokens replaces plaintext tokens left by older versions with their
// hashes, once, so upgrading signs nobody out. A kv marker records that it
// ran, since a raw token and a hash are both hex strings.
func migrateTokens(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var done int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM kv WHERE key='tokens_hashed'`).Scan(&done); err != nil || done > 0 {
		return err
	}
	rows, err := tx.Query(`SELECT token FROM tokens`)
	if err != nil {
		return err
	}
	var raw []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		raw = append(raw, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range raw {
		if _, err := tx.Exec(`UPDATE tokens SET token=? WHERE token=?`, hashToken(t), t); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO kv(key,value) VALUES('tokens_hashed','1')`); err != nil {
		return err
	}
	return tx.Commit()
}
