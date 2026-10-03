package stream

import (
	"crypto/subtle"
	"errors"
	"fmt"
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
	ID         string      `json:"id"`
	UserID     int64       `json:"userId"`
	UserName   string      `json:"userName"`
	ItemID     int64       `json:"itemId"`
	FileID     int64       `json:"fileId"`
	Title      string      `json:"title"`
	Subtitle   string      `json:"subtitle"`
	Method     string      `json:"method"`
	Reasons    []string    `json:"reasons"`
	VideoIn    string      `json:"videoIn"`
	AudioIn    string      `json:"audioIn"`
	VideoOut   string      `json:"videoOut"`
	AudioOut   string      `json:"audioOut"`
	Container  string      `json:"container"`
	SrcBitrate int         `json:"srcBitrate"`
	OutBitrate int         `json:"outBitrate"`
	Client     string      `json:"client"`
	IP         string      `json:"ip"`
	Remote     bool        `json:"remote"`
	StartedAt  int64       `json:"startedAt"`
	LastSeen   int64       `json:"lastSeen"`
	Position   float64     `json:"position"`
	Duration   float64     `json:"duration"`
	Paused     bool        `json:"paused"`
	Bytes      int64       `json:"bytes"`
	Rate       float64     `json:"rate"` // bytes/s delivered by the server, smoothed
	Watched    float64     `json:"watched"`
	Client_    ClientStats `json:"clientStats"`
	Job        *Progress   `json:"job,omitempty"`
	Restarts   int         `json:"restarts"`
	Cached     bool        `json:"cached"`
	HLS        bool        `json:"hls"`
	// AuthToken is the login token that opened (or last re-planned) the
	// session; the HLS stream key is only valid while it is.
	AuthToken string `json:"-"`
	streamKey string

	bytes     int64
	lastBytes int64
	lastTick  time.Time
	job       *Job
	playedSet bool
	lastPos   float64
	lastBeat  time.Time
	active    int         // open HTTP responses
	jobMu     *sync.Mutex // serialises seeks/replacements for this session
	ended     chan struct{}
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
}

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
func (m *Manager) Open(s *Session) (*Session, error) {
	if s.ID == "" || len(s.ID) > 128 {
		return nil, errors.New("session ID must be between 1 and 128 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.killed[s.ID]; ok {
		return nil, ErrStopped
	}
	now := time.Now()
	if old, ok := m.m[s.ID]; ok {
		if old.UserID != s.UserID || old.ItemID != s.ItemID {
			return nil, errors.New("session belongs to another user or item")
		}
		// Plan changed mid-playback (quality/audio switch): keep counters.
		old.Method, old.Reasons, old.VideoOut, old.AudioOut, old.OutBitrate = s.Method, s.Reasons, s.VideoOut, s.AudioOut, s.OutBitrate
		old.FileID, old.VideoIn, old.AudioIn, old.Container, old.SrcBitrate = s.FileID, s.VideoIn, s.AudioIn, s.Container, s.SrcBitrate
		old.Cached, old.HLS = s.Cached, s.HLS
		if s.AuthToken != "" {
			old.AuthToken = s.AuthToken
		}
		old.LastSeen = now.Unix()
		return old, nil
	}
	s.StartedAt, s.LastSeen = now.Unix(), now.Unix()
	s.streamKey = store.RandomToken(16)
	s.lastTick, s.lastBeat = now, now
	s.jobMu = &sync.Mutex{}
	s.ended = make(chan struct{})
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
	if s == nil || s.UserID != uid {
		m.mu.Unlock()
		return nil, errors.New("unknown session")
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
	s.lastPos, s.lastBeat = pos, now
	s.Position, s.Paused, s.Client_, s.LastSeen = pos, paused, cs, now.Unix()
	itemID, dur, playedSet := s.ItemID, s.Duration, s.playedSet
	m.mu.Unlock()

	if dur > 0 && pos/dur >= 0.92 || (dur > 1200 && dur-pos < 120 && pos > 0) {
		if !playedSet {
			m.st.MarkPlayed(uid, itemID, true)
			m.mu.Lock()
			s.playedSet = true
			m.mu.Unlock()
		}
	} else if !playedSet && pos >= 30 {
		m.st.SaveProgress(uid, itemID, pos)
	}
	m.mu.Lock()
	copy := *s
	m.mu.Unlock()
	return &copy, nil
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

func (m *Manager) finish(s *Session) {
	if m.OnEnd != nil {
		go m.OnEnd(s.ID)
	}
	m.mu.Lock()
	copy := *s
	m.mu.Unlock()
	s = &copy
	bytes := s.bytes
	if s.Watched < 10 {
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
	m.log.Infof("playback ended: %s — %s (%s, %.0fs watched, %d MB)", s.UserName, s.Title, s.Method, s.Watched, bytes>>20)
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
			Bytes: s.bytes, Rate: s.Rate, Watched: s.Watched, Client_: s.Client_, Restarts: s.Restarts, Cached: s.Cached, HLS: s.HLS,
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
	if ok {
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
