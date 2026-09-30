package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "data ? #"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestInitialAdminIsAtomic(t *testing.T) {
	st := testStore(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Go(func() { _, err := st.CreateInitialAdmin("admin", "test-password-123"); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSetupCompleted) {
			t.Errorf("unexpected setup error: %v", err)
		}
	}
	if n, err := st.UserCount(); err != nil || n != 1 || successes != 1 {
		t.Fatalf("users=%d successes=%d err=%v", n, successes, err)
	}
}

func TestPasswordPolicyAndRevocation(t *testing.T) {
	st := testStore(t)
	for _, password := range []string{"short", strings.Repeat("a", 73)} {
		if _, err := st.CreateUser("test", password, true); err == nil {
			t.Error("accepted invalid password")
		}
	}
	u, err := st.CreateUser("admin", "test-password-123", true)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.CreateToken(u.ID, "Test client", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(u.ID, "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TokenUser(token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old session survived: %v", err)
	}
	if _, err := st.Authenticate("admin", "new-password-123"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(999, "new-password-123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
}

func TestLastAdminCannotBeRemovedConcurrently(t *testing.T) {
	st := testStore(t)
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "demote", true: "delete"}[remove], func(t *testing.T) {
			st.DB().Exec("DELETE FROM users")
			a, err := st.CreateUser("admin-a", "test-password-123", true)
			if err != nil {
				t.Fatal(err)
			}
			b, err := st.CreateUser("admin-b", "test-password-123", true)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for _, id := range []int64{a.ID, b.ID} {
				wg.Go(func() {
					if remove {
						_ = st.DeleteUser(id)
					} else {
						_ = st.SetAdmin(id, false)
					}
				})
			}
			wg.Wait()
			if n, err := st.CountAdmins(); err != nil || n != 1 {
				t.Fatalf("admins=%d err=%v", n, err)
			}
		})
	}
}

func TestDatabasePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, path := range []string{dir, filepath.Join(dir, "lex.db"), filepath.Join(dir, "lex.db-wal"), filepath.Join(dir, "lex.db-shm")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Errorf("%s permissions=%o", filepath.Base(path), info.Mode().Perm())
		}
	}
}
