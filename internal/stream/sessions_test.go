package stream

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionOwnership(t *testing.T) {
	m := &Manager{m: map[string]*Session{}}
	original, err := m.Open(&Session{ID: "session", UserID: 1, ItemID: 10, FileID: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{{ID: "session", UserID: 2, ItemID: 10, FileID: 100}, {ID: "session", UserID: 1, ItemID: 20, FileID: 200}} {
		if _, err := m.Open(s); err == nil {
			t.Fatal("session takeover accepted")
		}
	}
	if m.Get("session").UserID != 1 {
		t.Fatal("session owner changed")
	}
	for _, pair := range [][2]int64{{2, 100}, {1, 200}} {
		if _, err := m.Find("session", pair[0], pair[1]); err == nil {
			t.Fatal("foreign session/file accepted")
		}
	}
	if got, err := m.Find("session", 1, 100); err != nil || got != original {
		t.Fatal("owner cannot retrieve session")
	}
	copy := m.Get("session")
	copy.UserID = 99
	if m.Get("session").UserID != 1 {
		t.Fatal("Get exposed mutable session")
	}
}

func TestConcurrentTranscodeLimitAndRemux(t *testing.T) {
	m := &Manager{m: map[string]*Session{}}
	var wg sync.WaitGroup
	var started atomic.Int32
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		s, err := m.Open(&Session{ID: fmt.Sprintf("session-%d", i), UserID: 1, ItemID: int64(i)})
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			_, err := m.AttachJob(s, 1, true, func() (*Job, error) {
				started.Add(1)
				return &Job{done: make(chan struct{}), Params: Params{VideoCopy: false}}, nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	}
	if got := started.Load(); got != 1 {
		t.Fatalf("started %d transcodes; want 1", got)
	}
	s, _ := m.Open(&Session{ID: "remux", UserID: 1, ItemID: 99})
	if _, err := m.AttachJob(s, 1, false, func() (*Job, error) { return &Job{done: make(chan struct{}), Params: Params{VideoCopy: true}}, nil }); err != nil {
		t.Fatalf("remux blocked by transcode limit: %v", err)
	}
}

func TestKilledSessionStaysStopped(t *testing.T) {
	m := &Manager{m: map[string]*Session{}}
	if _, err := m.Open(&Session{ID: "s", UserID: 1, ItemID: 10, FileID: 100}); err != nil {
		t.Fatal(err)
	}
	if !m.Kill("s") {
		t.Fatal("Kill of a live session reported false")
	}
	if m.Get("s") != nil {
		t.Fatal("killed session still listed")
	}
	// The player's next media request, heartbeat or replan must not revive it.
	if _, err := m.Find("s", 1, 100); !errors.Is(err, ErrStopped) {
		t.Fatalf("Find after Kill: %v, want ErrStopped", err)
	}
	if _, err := m.Heartbeat("s", 1, 50, false, ClientStats{}); !errors.Is(err, ErrStopped) {
		t.Fatalf("Heartbeat after Kill: %v, want ErrStopped", err)
	}
	if _, err := m.Open(&Session{ID: "s", UserID: 1, ItemID: 10, FileID: 100}); !errors.Is(err, ErrStopped) {
		t.Fatalf("Open after Kill: %v, want ErrStopped", err)
	}
	if m.Active() != 0 {
		t.Fatal("killed session re-created")
	}
	// A new play gets a new id and is unaffected.
	if _, err := m.Open(&Session{ID: "fresh", UserID: 1, ItemID: 10, FileID: 100}); err != nil {
		t.Fatalf("new session after Kill: %v", err)
	}
	// Unknown ids are not tombstoned.
	if m.Kill("nope") {
		t.Fatal("Kill of an unknown session reported true")
	}
	if _, err := m.Open(&Session{ID: "nope", UserID: 1, ItemID: 10, FileID: 100}); err != nil {
		t.Fatalf("unknown id blocked: %v", err)
	}
	// The tombstone expires.
	m.pruneKilled(time.Now().Add(killedTTL + time.Second))
	if _, err := m.Find("s", 1, 100); err != nil {
		t.Fatalf("tombstone not pruned: %v", err)
	}
}

// anon- ids are shared by every sid-less request for a file: a stop ends
// the current one without blocking that file for the user afterwards.
func TestKillDoesNotBlockAnonSessions(t *testing.T) {
	m := &Manager{m: map[string]*Session{}}
	if _, err := m.Open(&Session{ID: "anon-100-1", UserID: 1, ItemID: 10, FileID: 100}); err != nil {
		t.Fatal(err)
	}
	if !m.Kill("anon-100-1") {
		t.Fatal("kill: no session")
	}
	if _, err := m.Open(&Session{ID: "anon-100-1", UserID: 1, ItemID: 10, FileID: 100}); err != nil {
		t.Fatalf("anon session blocked after a stop: %v", err)
	}
}
