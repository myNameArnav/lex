package stream

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

// ClientStats is reported by the player in heartbeats.
type ClientStats struct {
	BufferAhead   float64 `json:"bufferAhead"`
	Bandwidth     float64 `json:"bandwidth"` // bits/s measured by the client
	DroppedFrames int     `json:"droppedFrames"`
	TotalFrames   int     `json:"totalFrames"`
	Buffering     bool    `json:"buffering"`
	BufferEvents  int     `json:"bufferEvents"`
	BufferSeconds float64 `json:"bufferSeconds"`
	Resolution    string  `json:"resolution"`
	Volume        float64 `json:"volume"`
	Rate          float64 `json:"rate"`
}

type Session struct {
	ID         string   `json:"id"`
	UserID     int64    `json:"userId"`
	UserName   string   `json:"userName"`
	ItemID     int64    `json:"itemId"`
	FileID     int64    `json:"fileId"`
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle"`
	Method     string   `json:"method"`
	Reasons    []string `json:"reasons"`
	VideoIn    string   `json:"videoIn"`
	AudioIn    string   `json:"audioIn"`
	VideoOut   string   `json:"videoOut"`
	AudioOut   string   `json:"audioOut"`
	Container  string   `json:"container"`
	SrcBitrate int      `json:"srcBitrate"`
	OutBitrate int      `json:"outBitrate"`
	Client     string   `json:"client"`
	IP         string   `json:"ip"`
	Remote     bool     `json:"remote"`
	StartedAt  int64    `json:"startedAt"`
	LastSeen   int64    `json:"lastSeen"`
	Position   float64  `json:"position"`
	Duration   float64  `json:"duration"`
	Paused     bool     `json:"paused"`
	Bytes      int64    `json:"bytes"`
	Rate       float64  `json:"rate"` // bytes/s delivered by the server, smoothed
	// DiskWait is the share (0-1, smoothed) of the time spent serving the
	// file that went into reading it from disk rather than sending it to
	// the player (ioclock.go). Near 1, the server's disk is the bottleneck,
	// not the viewer's connection. Only the file route measures it.
	DiskWait float64     `json:"diskWait"`
	Watched  float64     `json:"watched"`
	Client_  ClientStats `json:"clientStats"`
	Job      *Progress   `json:"job,omitempty"`
	Restarts int         `json:"restarts"`
	Cached   bool        `json:"cached"`
	HLS      bool        `json:"hls"`
	// Live is Live TV: tuning in mid-program says nothing about what the
	// viewer has watched, so it saves no resume position or watched mark.
	// Set from the id (LivePrefix), so a session re-opened after a server
	// restart stays live.
	Live bool `json:"live"`
	// Extra is a local extra (trailer, featurette, ...): watching one saves
	// no resume position, watched mark or history for its title.
	Extra bool `json:"extra"`
	// CreditsAt is where the end credits start (detected, or a Credits
	// chapter), or 0 when unknown. Reaching it marks the title watched.
	CreditsAt float64 `json:"-"`
	// AuthToken is the login token that opened (or last re-planned) the
	// session; the HLS stream key is only valid while it is.
	AuthToken string `json:"-"`
	streamKey string

	bytes     int64
	lastBytes int64
	io        *sessionIO // disk and network timing (ioclock.go)
	lastDisk  int64
	lastNet   int64
	lastTick  time.Time
	job       *Job
	playedSet bool
	wasPlayed bool // the item's watched state before this session marked it
	lastPos   float64
	lastBeat  time.Time
	active    int         // open HTTP responses
	jobMu     *sync.Mutex // serialises seeks/replacements for this session
	ended     chan struct{}

	// The no-progress watchdog (Heartbeat): when the position last moved
	// (or playback paused), the bytes delivered by then, and whether this
	// stall was logged.
	movedAt    time.Time
	movedBytes int64
	stuckLog   bool
	// Player events allowed (EventAllowed), refilled over time.
	eventBudget float64
	eventAt     time.Time
}

// Ended is closed when the session ends (player closed, expired or stopped
// by an admin), so open media responses can be cut off.
func (s *Session) Ended() <-chan struct{} { return s.ended }

func (s *Session) end() {
	if s.ended != nil {
		close(s.ended)
	}
}

type Manager struct {
	// OnEnd is called when a session ends (player closed or expired).
	OnEnd func(id string)
	// Slots enforces the transcode limit together with HLS jobs.
	Slots *Transcodes
	st    *store.Store
	log   *logx.Logger
	mu    sync.Mutex
	m     map[string]*Session
	// killed remembers sessions an admin stopped, so the player's next media
	// request or heartbeat can't quietly re-open them.
	killed map[string]time.Time
	jobSeq int
	closed bool // Shutdown ran: the server is going away
}

// ErrShuttingDown is returned when a session is opened during shutdown.
var ErrShuttingDown = errors.New("server is shutting down")

// ErrUnknownSession is returned for a heartbeat whose session isn't open,
// e.g. after a server restart.
var ErrUnknownSession = errors.New("unknown session")

// ErrStopped is returned for a session an admin stopped from the dashboard.
var ErrStopped = errors.New("Playback was stopped by the server admin")

// killedTTL is how long a stopped session id stays blocked. The player gets
// a fresh id whenever it is opened again, so this only needs to outlast the
// old player's retries.
const killedTTL = 10 * time.Minute

func NewManager(st *store.Store, log *logx.Logger) *Manager {
	mgr := &Manager{st: st, log: log, m: map[string]*Session{}, killed: map[string]time.Time{}, Slots: &Transcodes{}}
	go mgr.loop()
	return mgr
}

// Open registers (or refreshes) a session when a plan is created.
// LivePrefix starts the id of every Live TV session.
const LivePrefix = "live-"

func (m *Manager) Open(s *Session) (*Session, error) {
	if s.ID == "" || len(s.ID) > 128 {
		return nil, errors.New("session ID must be between 1 and 128 bytes")
	}
	s.Live = strings.HasPrefix(s.ID, LivePrefix)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.killed[s.ID]; ok {
		return nil, ErrStopped
	}
	if m.closed {
		return nil, ErrShuttingDown
	}
	now := time.Now()
	if old, ok := m.m[s.ID]; ok {
		if old.UserID != s.UserID || old.ItemID != s.ItemID {
			return nil, errors.New("session belongs to another user or item")
		}
		// Plan changed mid-playback (quality/audio switch): keep counters.
		old.Method, old.Reasons, old.VideoOut, old.AudioOut, old.OutBitrate = s.Method, s.Reasons, s.VideoOut, s.AudioOut, s.OutBitrate
		old.FileID, old.VideoIn, old.AudioIn, old.Container, old.SrcBitrate = s.FileID, s.VideoIn, s.AudioIn, s.Container, s.SrcBitrate
		old.Cached, old.HLS, old.Extra = s.Cached, s.HLS, s.Extra
		if s.CreditsAt > 0 {
			old.CreditsAt = s.CreditsAt
		}
		if s.AuthToken != "" {
			old.AuthToken = s.AuthToken
		}
		old.LastSeen = now.Unix()
		return old, nil
	}
	s.StartedAt, s.LastSeen = now.Unix(), now.Unix()
	s.streamKey = store.RandomToken(16)
	s.lastTick, s.lastBeat, s.movedAt = now, now, now
	s.jobMu = &sync.Mutex{}
	s.ended = make(chan struct{})
	s.io = &sessionIO{}
	m.m[s.ID] = s
	return s, nil
}

func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.m[id]; s != nil {
		copy := *s
		return &copy
	}
	return nil
}

// Find returns the live session only after checking its owner and media file.
func (m *Manager) Find(id string, uid, fileID int64) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.killed[id]; ok {
		return nil, ErrStopped
	}
	s := m.m[id]
	if s != nil && (s.UserID != uid || s.FileID != fileID) {
		return nil, errors.New("session belongs to another user or file")
	}
	return s, nil
}

var ErrLimit = errors.New("transcode limit reached: too many simultaneous transcodes on this server")

// AttachJob replaces the session's ffmpeg job (e.g. after a seek), enforcing
// the concurrent-transcode limit for video transcodes.
func (m *Manager) AttachJob(s *Session, maxTranscodes int, transcode bool, start func() (*Job, error)) (*Job, error) {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	m.mu.Lock()
	old := s.job
	s.job = nil
	m.mu.Unlock()
	if old != nil {
		old.Stop()
		select {
		case <-old.Done():
		case <-time.After(3 * time.Second):
		}
		if old.slot != "" {
			m.Slots.Release(old.slot)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m[s.ID] != s {
		return nil, errors.New("session ended")
	}
	// One limit across MSE and HLS transcodes: each running transcode holds
	// a slot until its ffmpeg exits.
	if m.Slots == nil {
		m.Slots = &Transcodes{}
	}
	slots, slot := m.Slots, ""
	if transcode {
		m.jobSeq++
		slot = fmt.Sprintf("mse:%s:%d", s.ID, m.jobSeq)
		if !slots.Acquire(slot, maxTranscodes) {
			return nil, ErrLimit
		}
	}
	j, err := start()
	if err != nil {
		if slot != "" {
			slots.Release(slot)
		}
		return nil, err
	}
	if slot != "" {
		j.slot = slot
		go func() { <-j.Done(); slots.Release(slot) }()
	}
	if old != nil {
		s.Restarts++
	}
	s.job = j
	return j, nil
}

// DetachJob clears the job if it's still the current one.
func (m *Manager) DetachJob(s *Session, j *Job) {
	m.mu.Lock()
	if s.job == j {
		s.job = nil
	}
	m.mu.Unlock()
}

func (m *Manager) AddBytes(s *Session, n int) {
	m.mu.Lock()
	s.bytes += int64(n)
	m.mu.Unlock()
}

// DiskBound reports whether an open stream of another file than fileID is
// mostly waiting on the disk (DiskWait), so background disk work (the
// media cache copying fileID) can wait for it.
func (m *Manager) DiskBound(fileID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.m {
		if s.active > 0 && s.DiskWait >= 0.5 && s.FileID != fileID {
			return true
		}
	}
	return false
}

// Health is how fast the server is sending the session's stream (bytes/s)
// and its DiskWait.
// s may be a copy (Get), so this reads the live session.
func (m *Manager) Health(s *Session) (rate, diskWait float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if live := m.m[s.ID]; live != nil {
		s = live
	}
	return s.Rate, s.DiskWait
}

func (m *Manager) Touch(s *Session, delta int) {
	m.mu.Lock()
	s.active += delta
	s.LastSeen = time.Now().Unix()
	m.mu.Unlock()
}

// Heartbeat records playback position and client stats.
func (m *Manager) Heartbeat(id string, uid int64, pos float64, paused bool, cs ClientStats) (*Session, error) {
	m.mu.Lock()
	if _, ok := m.killed[id]; ok {
		m.mu.Unlock()
		return nil, ErrStopped
	}
	s := m.m[id]
	if s == nil {
		m.mu.Unlock()
		return nil, ErrUnknownSession
	}
	if s.UserID != uid {
		m.mu.Unlock()
		return nil, errors.New("session belongs to another user")
	}
	now := time.Now()
	if !s.Paused && !paused {
		// Count real watch time: bounded by wall time since last beat.
		dt := now.Sub(s.lastBeat).Seconds()
		dp := pos - s.lastPos
		if dp > 0 && dt > 0 {
			rate := cs.Rate
			if rate <= 0 {
				rate = 1
			}
			s.Watched += min(dp, dt*rate+2)
		}
	}
	stuck, resumed := s.watchProgress(now, pos, paused)
	s.lastPos, s.lastBeat = pos, now
	s.Position, s.Paused, s.Client_, s.LastSeen = pos, paused, cs, now.Unix()
	itemID, dur, credits, playedSet, live, watched := s.ItemID, s.Duration, s.CreditsAt, s.playedSet, s.Live || s.Extra, s.Watched
	user, title, method, client := s.UserName, s.Title, s.Method, s.Client
	m.mu.Unlock()
	if stuck != "" {
		m.log.Warnf("playback stuck: %s — %s (%s, %s): %s", user, title, method, client, stuck)
	}
	if resumed != "" {
		m.log.Infof("playback moving again: %s — %s: %s", user, title, resumed)
	}

	switch {
	case live:
	case nearEnd(pos, dur, credits):
		// Reaching the end by scrubbing isn't watching it: wait until the
		// session has some real playback behind it.
		if !playedSet && watched >= minWatchedToMark {
			prev, _ := m.st.GetUserData(uid, itemID)
			m.st.MarkPlayed(uid, itemID, true)
			m.mu.Lock()
			s.playedSet, s.wasPlayed = true, prev.Played
			m.mu.Unlock()
		}
	case playedSet:
		// The viewer went back after the mark: undo it and keep the resume
		// point, as if the end had never been reached.
		m.mu.Lock()
		wasPlayed := s.wasPlayed
		s.playedSet = false
		m.mu.Unlock()
		m.st.UndoPlayed(uid, itemID, wasPlayed, pos)
	case pos >= 30:
		m.st.SaveProgress(uid, itemID, pos)
	}
	m.mu.Lock()
	copy := *s
	m.mu.Unlock()
	return &copy, nil
}

// minWatchedToMark is how much real playback a session needs before reaching
// the end marks the title watched.
const minWatchedToMark = 30

// nearEnd reports whether a position counts as finished. When the end
// credits' start is known, that is reaching the credits. Otherwise it is the
// last 8% of the runtime, but never more than five minutes before the end
// (so stopping 12 minutes before the end of a long film keeps the resume
// point) and, for anything over 20 minutes, at least the last two minutes.
func nearEnd(pos, dur, credits float64) bool {
	if dur <= 0 || pos <= 0 {
		return false
	}
	// Credits in the second half only: an early "credits" mark is a bad
	// detection or chapter name, not the end of the story. A couple of
	// seconds' slack covers a stop right as they begin.
	if credits >= dur/2 && credits < dur {
		return pos >= credits-2
	}
	left := min(dur*0.08, 300)
	if dur > 1200 {
		left = max(left, 120)
	}
	return dur-pos <= left
}

// A session whose position hasn't moved for stuckAfter while unpaused, with
// at least stuckBytes delivered meanwhile, is downloading without playing
// (e.g. the browser throwing away what it's sent): worth a warning.
const (
	stuckAfter = 60 * time.Second
	stuckBytes = 64 << 20
)

// watchProgress runs the no-progress watchdog for a heartbeat (m.mu held).
// It returns a message the first time a stall qualifies, and one when the
// position moves again after that.
func (s *Session) watchProgress(now time.Time, pos float64, paused bool) (stuck, resumed string) {
	if s.movedAt.IsZero() {
		s.movedAt, s.movedBytes = now, s.bytes
	}
	if paused || s.Paused || math.Abs(pos-s.lastPos) > 0.5 {
		if s.stuckLog && !paused {
			resumed = fmt.Sprintf("at %s after %s", Clock(pos), now.Sub(s.movedAt).Round(time.Second))
		}
		s.movedAt, s.movedBytes, s.stuckLog = now, s.bytes, false
		return
	}
	waited, sent := now.Sub(s.movedAt), s.bytes-s.movedBytes
	if !s.stuckLog && waited >= stuckAfter && sent >= stuckBytes {
		s.stuckLog = true
		cs := s.Client_
		stuck = fmt.Sprintf("no progress at %s for %s while %d MB was delivered; client reports %.0fs buffered ahead, buffering=%v, %d stalls",
			Clock(pos), waited.Round(time.Second), sent>>20, cs.BufferAhead, cs.Buffering, cs.BufferEvents)
	}
	return
}

// Clock formats seconds as m:ss or h:mm:ss.
func Clock(t float64) string {
	n := int(max(t, 0))
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
	}
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

// EventAllowed rate-limits the player events a session may log: a burst of
// 20, then one every 3 seconds. Unknown or foreign sessions get false.
func (m *Manager) EventAllowed(id string, uid int64) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.m[id]
	if s == nil || s.UserID != uid {
		return nil, false
	}
	now := time.Now()
	if s.eventAt.IsZero() {
		s.eventBudget = 20
	} else {
		s.eventBudget = min(20, s.eventBudget+now.Sub(s.eventAt).Seconds()/3)
	}
	s.eventAt = now
	copy := *s
	if s.eventBudget < 1 {
		return &copy, false
	}
	s.eventBudget--
	return &copy, true
}

// Stop ends a session immediately (player closed).
func (m *Manager) Stop(id string, uid int64) {
	m.mu.Lock()
	s := m.m[id]
	if s == nil || (uid != 0 && s.UserID != uid) {
		m.mu.Unlock()
		return
	}
	delete(m.m, id)
	s.end()
	j := s.job
	m.mu.Unlock()
	if j != nil {
		j.Stop()
	}
	m.finish(s)
}

// Shutdown ends every open session the way a closed player would: jobs
// stopped, OnEnd run (synchronously, so HLS encoders are stopped before the
// process exits) and a history row written. Sessions can't be opened after.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	open := make([]*Session, 0, len(m.m))
	for id, s := range m.m {
		delete(m.m, id)
		s.end()
		open = append(open, s)
	}
	m.mu.Unlock()
	for _, s := range open {
		if s.job != nil {
			s.job.Stop()
		}
		if m.OnEnd != nil {
			m.OnEnd(s.ID)
		}
		m.record(s)
	}
}

func (m *Manager) finish(s *Session) {
	if m.OnEnd != nil {
		go m.OnEnd(s.ID)
	}
	m.record(s)
}

// record logs an ended session and writes its history row.
func (m *Manager) record(s *Session) {
	m.mu.Lock()
	copy := *s
	m.mu.Unlock()
	s = &copy
	bytes := s.bytes
	// Logged even when too short for the history: a session that never
	// got going is what needs looking into.
	if m.log != nil {
		m.log.Infof("playback ended: %s — %s (%s, %.0fs watched, %d MB, %s, %d stalls/%.0fs, %d stream restarts)",
			s.UserName, s.Title, s.Method, s.Watched, bytes>>20, s.Client, s.Client_.BufferEvents, s.Client_.BufferSeconds, s.Restarts)
	}
	if s.Watched < 10 || s.Extra {
		return
	}
	h := &store.HistoryEntry{
		UserID: s.UserID, UserName: s.UserName, ItemID: s.ItemID, Title: s.Title, FileID: s.FileID,
		StartedAt: s.StartedAt, EndedAt: time.Now().Unix(), Watched: s.Watched, Method: s.Method,
		Reasons: strings.Join(s.Reasons, "; "), Client: s.Client, IP: s.IP, Remote: s.Remote, Bytes: bytes,
		VideoOut: s.VideoOut, AudioOut: s.AudioOut, BufferEvents: s.Client_.BufferEvents, BufferSeconds: s.Client_.BufferSeconds,
	}
	if err := m.st.AddHistory(h); err != nil {
		m.log.Warnf("history: %v", err)
	}
}

// Snapshot returns copies of all sessions for the dashboard.
func (m *Manager) Snapshot() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.m))
	for _, s := range m.m {
		c := Session{
			ID: s.ID, UserID: s.UserID, UserName: s.UserName, ItemID: s.ItemID, FileID: s.FileID, Title: s.Title, Subtitle: s.Subtitle,
			Method: s.Method, Reasons: s.Reasons, VideoIn: s.VideoIn, AudioIn: s.AudioIn, VideoOut: s.VideoOut, AudioOut: s.AudioOut,
			Container: s.Container, SrcBitrate: s.SrcBitrate, OutBitrate: s.OutBitrate, Client: s.Client, IP: s.IP, Remote: s.Remote,
			StartedAt: s.StartedAt, LastSeen: s.LastSeen, Position: s.Position, Duration: s.Duration, Paused: s.Paused,
			Bytes: s.bytes, Rate: s.Rate, DiskWait: s.DiskWait, Watched: s.Watched, Client_: s.Client_, Restarts: s.Restarts, Cached: s.Cached, HLS: s.HLS, Live: s.Live,
		}
		if s.job != nil {
			p := s.job.Progress()
			c.Job = &p
		}
		out = append(out, c)
	}
	return out
}

// ActiveJobs returns the number of running ffmpeg jobs (copy, transcode).
func (m *Manager) ActiveJobs() (remux, transcode int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.m {
		if s.job == nil {
			continue
		}
		select {
		case <-s.job.Done():
			continue
		default:
		}
		if s.job.Params.VideoCopy {
			remux++
		} else {
			transcode++
		}
	}
	return
}

// StreamKey returns the per-session key that authorises HLS requests from
// native players that don't send cookies.
func (m *Manager) StreamKey(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.m[id]; s != nil {
		return s.streamKey
	}
	return ""
}

// KeyUser resolves a stream key to its session's user and the login token
// behind it, which the caller must check is still valid (so signing out or
// a password reset also ends HLS access).
func (m *Manager) KeyUser(sid, key string) (int64, string) {
	if key == "" {
		return 0, ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.m[sid]; s != nil && s.streamKey != "" && subtle.ConstantTimeCompare([]byte(s.streamKey), []byte(key)) == 1 {
		return s.UserID, s.AuthToken
	}
	return 0, ""
}

// Active returns the number of playback sessions.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.m)
}

// TotalRate is the summed outbound streaming rate in bytes/s.
func (m *Manager) TotalRate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := 0.0
	for _, s := range m.m {
		t += s.Rate
	}
	return t
}

// Kill stops a session from the dashboard and blocks its id for killedTTL.
func (m *Manager) Kill(id string) bool {
	m.mu.Lock()
	_, ok := m.m[id]
	// anon- ids are shared by every sid-less request for a file, so they
	// aren't blocked: the stop only ends what is playing now.
	if ok && !strings.HasPrefix(id, "anon-") {
		if m.killed == nil {
			m.killed = map[string]time.Time{}
		}
		m.killed[id] = time.Now()
	}
	m.mu.Unlock()
	if ok {
		m.Stop(id, 0)
	}
	return ok
}

func (m *Manager) loop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for now := range t.C {
		var ended []*Session
		m.mu.Lock()
		m.pruneKilled(now)
		for id, s := range m.m {
			b := s.bytes
			if dt := now.Sub(s.lastTick).Seconds(); dt > 0 {
				inst := float64(b-s.lastBytes) / dt
				s.Rate = s.Rate*0.6 + inst*0.4
			}
			s.lastBytes, s.lastTick = b, now
			s.sampleIO(now)
			if s.job != nil {
				s.job.SampleCPU()
			}
			// Idle sessions (no heartbeat, no open stream) expire. Paused
			// players still heartbeat, so they survive.
			if s.active <= 0 && now.Unix()-s.LastSeen > 45 {
				delete(m.m, id)
				s.end()
				ended = append(ended, s)
			}
		}
		m.mu.Unlock()
		for _, s := range ended {
			if s.job != nil {
				s.job.Stop()
			}
			m.finish(s)
		}
	}
}

func (m *Manager) pruneKilled(now time.Time) {
	for id, at := range m.killed {
		if now.Sub(at) > killedTTL {
			delete(m.killed, id)
		}
	}
}
