package store

import (
	"errors"
	"strings"
	"testing"
)

// Only the token's hash is stored; the raw token still signs in, and the
// devices list names sessions by a prefix of the hash.
func TestTokensStoredHashed(t *testing.T) {
	st := testStore(t)
	u, err := st.CreateUser("admin", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.CreateToken(u.ID, "Firefox", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := st.CreateToken(u.ID, "Safari", "127.0.0.2")
	var stored string
	if err := st.DB().QueryRow(`SELECT token FROM tokens WHERE client='Firefox'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == tok || strings.Contains(stored, tok) || stored != hashToken(tok) {
		t.Fatalf("stored %q for token %q", stored, tok)
	}
	if got, err := st.TokenUser(tok); err != nil || got.ID != u.ID {
		t.Fatalf("raw token: %v %v", got, err)
	}
	if _, err := st.TokenUser(stored); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the stored hash signed in: %v", err)
	}

	devs, err := st.Tokens()
	if err != nil || len(devs) != 2 {
		t.Fatalf("devices: %v %v", devs, err)
	}
	for _, d := range devs {
		if len(d.Prefix) != DeviceIDLen || strings.HasPrefix(tok, d.Prefix) || strings.HasPrefix(other, d.Prefix) {
			t.Fatalf("device id %q leaks a token", d.Prefix)
		}
	}
	for _, bad := range []string{"", tok[:8], strings.ToUpper(DeviceID(tok)), "%%%%%%%%%%%%%%%%"} {
		if err := st.DeleteTokenPrefix(bad); err == nil {
			t.Errorf("accepted device id %q", bad)
		}
	}
	// A raw-token prefix is well formed but names no device.
	if err := st.DeleteTokenPrefix(tok[:DeviceIDLen]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TokenUser(tok); err != nil {
		t.Fatal("a bad device id signed a session out")
	}
	if err := st.DeleteTokenPrefix(DeviceID(other)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TokenUser(other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("signed-out device still works: %v", err)
	}
	if _, err := st.TokenUser(tok); err != nil {
		t.Fatalf("signing out one device signed out another: %v", err)
	}
	if err := st.DeleteToken(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TokenUser(tok); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted token still works: %v", err)
	}
}

// Plaintext tokens from older versions are hashed in place on startup,
// exactly once, so nobody is signed out by the upgrade.
func TestOpenHashesPlaintextTokens(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser("admin", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	// What an older version left: a raw token and no migration marker.
	raw := RandomToken(24)
	if _, err := st.DB().Exec(`INSERT INTO tokens(token,user_id,created_at,last_seen,client,ip) VALUES(?,?,?,?,'Old','')`, raw, u.ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM kv WHERE key='tokens_hashed'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	for range 2 { // the second start must not hash the hash
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := st.TokenUser(raw); err != nil || got.ID != u.ID {
			t.Fatalf("old session after upgrade: %v %v", got, err)
		}
		var n int
		st.DB().QueryRow(`SELECT COUNT(*) FROM tokens WHERE token=?`, raw).Scan(&n)
		if n != 0 {
			t.Fatal("plaintext token left in the database")
		}
		st.Close()
	}
}
