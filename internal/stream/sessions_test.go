package stream

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
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

// Live TV joins programs mid-way: it saves no resume position and no
// watched mark, whatever the position. The id decides, not the field, so a
// session re-opened after a restart stays live.
func TestLiveSessionSavesNoProgress(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`,
		`INSERT INTO items(id,library_id,kind,title,sort_title,added_at,updated_at) VALUES(1,1,'movie','A','a',0,0),(2,1,'movie','B','b',0,0)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(st, logx.New(10, false))
	for _, s := range []*Session{
		{ID: LivePrefix + "1", UserID: u.ID, ItemID: 1, FileID: 1, Duration: 3000},
		{ID: "vod", Live: true, UserID: u.ID, ItemID: 2, FileID: 2, Duration: 3000},
	} {
		if _, err := m.Open(s); err != nil {
			t.Fatal(err)
		}
		for _, pos := range []float64{600, 2990} {
			if _, err := m.Heartbeat(s.ID, u.ID, pos, false, ClientStats{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ud, _ := st.GetUserData(u.ID, 1); ud != (store.UserData{}) {
		t.Fatalf("live session saved %+v", ud)
	}
	if ud, _ := st.GetUserData(u.ID, 2); !ud.Played {
		t.Fatalf("on-demand session saved %+v, want played", ud)
	}
}

// A session that keeps receiving bytes while its position stands still is
// logged once, and again when it moves; pauses and seeks don't count.
func TestWatchProgressFlagsDownloadsThatDontPlay(t *testing.T) {
	t0 := time.Now()
	s := &Session{lastPos: 6000, movedAt: t0}
	beat := func(after time.Duration, pos float64, paused bool, bytes int64) (string, string) {
		s.bytes = bytes
		stuck, resumed := s.watchProgress(t0.Add(after), pos, paused)
		s.lastPos, s.Paused = pos, paused
		return stuck, resumed
	}
	if stuck, _ := beat(30*time.Second, 6000, false, 500<<20); stuck != "" {
		t.Fatalf("logged before %s: %q", stuckAfter, stuck)
	}
	stuck, _ := beat(90*time.Second, 6000, false, 900<<20)
	if !strings.Contains(stuck, "no progress at 1:40:00 for 1m30s while 900 MB") {
		t.Fatalf("stuck message: %q", stuck)
	}
	if again, _ := beat(120*time.Second, 6000, false, 1000<<20); again != "" {
		t.Fatalf("logged the same stall twice: %q", again)
	}
	if _, resumed := beat(130*time.Second, 6005, false, 1000<<20); !strings.Contains(resumed, "at 1:40:05 after 2m10s") {
		t.Fatalf("resumed message: %q", resumed)
	}
	// Paused with a full buffer: nothing moves, nothing is wrong.
	beat(140*time.Second, 6005, true, 1000<<20)
	if stuck, _ := beat(300*time.Second, 6005, true, 2000<<20); stuck != "" {
		t.Fatalf("paused session logged: %q", stuck)
	}
	// Standing still with little delivered (slow network): not this warning.
	beat(310*time.Second, 6005, false, 2000<<20)
	if stuck, _ := beat(400*time.Second, 6005, false, 2010<<20); stuck != "" {
		t.Fatalf("slow session logged: %q", stuck)
	}
}

func TestEventAllowedRateLimits(t *testing.T) {
	m := &Manager{m: map[string]*Session{"s": {ID: "s", UserID: 1}}}
	if _, ok := m.EventAllowed("s", 2); ok {
		t.Fatal("another user's session accepted events")
	}
	if s, _ := m.EventAllowed("missing", 1); s != nil {
		t.Fatal("unknown session found")
	}
	n := 0
	for i := 0; i < 100; i++ {
		if _, ok := m.EventAllowed("s", 1); ok {
			n++
		}
	}
	if n != 20 {
		t.Fatalf("burst allowed %d events, want 20", n)
	}
	m.m["s"].eventAt = m.m["s"].eventAt.Add(-9 * time.Second)
	if _, ok := m.EventAllowed("s", 1); !ok {
		t.Fatal("budget didn't refill")
	}
}
