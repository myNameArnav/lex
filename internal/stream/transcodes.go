package stream

import "sync"

// Transcodes enforces the concurrent video-transcode limit across both
// delivery paths (MSE jobs and HLS jobs). Each running transcode holds one
// slot under a unique owner key until it stops.
type Transcodes struct {
	mu   sync.Mutex
	held map[string]bool
}

// Acquire takes a slot for owner, or reports false if limit (> 0) slots are
// already in use. Acquiring again for the same owner is a no-op.
func (t *Transcodes) Acquire(owner string, limit int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held == nil {
		t.held = map[string]bool{}
	}
	if t.held[owner] {
		return true
	}
	if limit > 0 && len(t.held) >= limit {
		return false
	}
	t.held[owner] = true
	return true
}

// Release frees owner's slot (safe to call more than once).
func (t *Transcodes) Release(owner string) {
	t.mu.Lock()
	delete(t.held, owner)
	t.mu.Unlock()
}

// Count returns the number of running transcodes.
func (t *Transcodes) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.held)
}
