// Package cache keeps copies of media files on a fast disk (e.g. an SSD) so
// playback reads from it instead of a slow or sleeping hard drive.
//
// Files are copied whole, in the background, at a limited rate, into a
// temporary name that is renamed once complete. Entries are evicted least
// recently used first to stay under the configured size and to keep a
// minimum of free space on the cache filesystem.
package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"lex/internal/logx"
	"lex/internal/store"
)

type Entry struct {
	FileID     int64  `json:"fileId"`
	ItemID     int64  `json:"itemId"`
	Title      string `json:"title"`
	Name       string `json:"name"`
	Path       string `json:"-"`
	Size       int64  `json:"size"`
	AddedAt    int64  `json:"addedAt"`
	LastAccess int64  `json:"lastAccess"`
	Hits       int    `json:"hits"`
	Reason     string `json:"reason"`
}

type Job struct {
	FileID  int64   `json:"fileId"`
	Name    string  `json:"name"`
	Size    int64   `json:"size"`
	Done    int64   `json:"done"`
	Speed   float64 `json:"speed"` // bytes/s
	Reason  string  `json:"reason"`
	Started int64   `json:"started"`
}

type Status struct {
	Enabled   bool   `json:"enabled"`
	Dir       string `json:"dir"`
	UsedBytes int64  `json:"usedBytes"`
	MaxBytes  int64  `json:"maxBytes"`
	Files     int    `json:"files"`
	DiskFree  int64  `json:"diskFree"`
	DiskTotal int64  `json:"diskTotal"`
	Current   *Job   `json:"current,omitempty"`
	Queued    []Job  `json:"queued"`
	Hits      int64  `json:"hits"`
	Misses    int64  `json:"misses"`
	LastError string `json:"lastError,omitempty"`
}

type Cache struct {
	st      *store.Store
	log     *logx.Logger
	dataDir string

	mu      sync.Mutex
	queue   []Job
	current *Job
	cancel  context.CancelFunc
	wake    chan struct{}
	touched map[int64]int64 // file id -> last persisted access (unix)
	hits    int64
	misses  int64
	lastErr string
	ready   map[int64]string // file id -> cached path (complete entries)
}

func New(st *store.Store, log *logx.Logger, dataDir string) *Cache {
	c := &Cache{st: st, log: log, dataDir: dataDir, wake: make(chan struct{}, 1), touched: map[int64]int64{}, ready: map[int64]string{}}
	c.reconcile()
	go c.worker()
	return c
}

// Dir is the effective cache directory. A configured directory always gets
// a dedicated "lex-cache" subfolder so we never touch unrelated files.
func (c *Cache) Dir() string {
	d := strings.TrimSpace(c.st.Config().CacheDir)
	if d == "" {
		return filepath.Join(c.dataDir, "cache")
	}
	return filepath.Join(d, "lex-cache")
}

var ownName = regexp.MustCompile(`^\d+(\.[a-z0-9]{1,5})?(\.part)?$`)

func (c *Cache) enabled() bool { return c.st.Config().CacheEnabled }

func (c *Cache) db() *sql.DB { return c.st.DB() }

// reconcile drops stale temp files, entries whose cached copy vanished and
// cached files the database doesn't know about.
func (c *Cache) reconcile() {
	dir := c.Dir()
	os.MkdirAll(dir, 0o755)
	known := map[string]bool{}
	rows, err := c.db().Query(`SELECT file_id, path FROM cache_entries`)
	if err != nil {
		return
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var p string
		rows.Scan(&id, &p)
		if _, err := os.Stat(p); err != nil {
			gone = append(gone, id)
			continue
		}
		known[p] = true
		c.ready[id] = p
	}
	rows.Close()
	for _, id := range gone {
		c.db().Exec(`DELETE FROM cache_entries WHERE file_id=?`, id)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		if !known[p] && !e.IsDir() && ownName.MatchString(e.Name()) {
			os.Remove(p)
		}
	}
}

// Resolve returns the path to read a file from: the cached copy if it's
// complete and still matches the source, otherwise the original.
func (c *Cache) Resolve(f *store.File) string {
	if !c.enabled() {
		return f.Path
	}
	c.mu.Lock()
	p, ok := c.ready[f.ID]
	c.mu.Unlock()
	if !ok {
		c.mu.Lock()
		c.misses++
		c.mu.Unlock()
		return f.Path
	}
	var size, mtime int64
	if err := c.db().QueryRow(`SELECT size, src_mtime FROM cache_entries WHERE file_id=?`, f.ID).Scan(&size, &mtime); err != nil || size != f.Size || mtime != f.Mtime {
		// Source replaced (e.g. a quality upgrade): drop the stale copy.
		c.Remove(f.ID)
		return f.Path
	}
	if st, err := os.Stat(p); err != nil || st.Size() != size {
		c.Remove(f.ID)
		return f.Path
	}
	now := time.Now().Unix()
	c.mu.Lock()
	c.hits++
	last := c.touched[f.ID]
	if now-last > 60 {
		c.touched[f.ID] = now
	}
	c.mu.Unlock()
	if now-last > 60 {
		c.db().Exec(`UPDATE cache_entries SET last_access=?, hits=hits+1 WHERE file_id=?`, now, f.ID)
	}
	return p
}

// Peek is Resolve without side effects (no hit counting or LRU touch), for
// background jobs.
func (c *Cache) Peek(f *store.File) string {
	if !c.enabled() {
		return f.Path
	}
	c.mu.Lock()
	p, ok := c.ready[f.ID]
	c.mu.Unlock()
	if !ok {
		return f.Path
	}
	if st, err := os.Stat(p); err != nil || st.Size() != f.Size {
		return f.Path
	}
	return p
}

// IsCached reports whether a complete copy exists.
func (c *Cache) IsCached(fileID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.ready[fileID]
	return ok
}

// Request queues a file for caching. It's a no-op if the cache is off, the
// file is cached/queued already, or it is too large.
func (c *Cache) Request(f *store.File, reason string) {
	cfg := c.st.Config()
	if !cfg.CacheEnabled || f == nil || f.Size <= 0 {
		return
	}
	if f.Size > int64(cfg.CacheMaxFileGB)<<30 || f.Size > int64(cfg.CacheMaxGB)<<30 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ready[f.ID]; ok {
		return
	}
	if c.current != nil && c.current.FileID == f.ID {
		return
	}
	for _, j := range c.queue {
		if j.FileID == f.ID {
			return
		}
	}
	job := Job{FileID: f.ID, Name: filepath.Base(f.Path), Size: f.Size, Reason: reason}
	// What's being watched right now jumps ahead of prefetches.
	if reason == "playing" {
		c.queue = append([]Job{job}, c.queue...)
	} else {
		c.queue = append(c.queue, job)
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Remove deletes a cached copy (or cancels its copy job).
func (c *Cache) Remove(fileID int64) {
	c.mu.Lock()
	if c.current != nil && c.current.FileID == fileID && c.cancel != nil {
		c.cancel()
	}
	for i, j := range c.queue {
		if j.FileID == fileID {
			c.queue = append(c.queue[:i], c.queue[i+1:]...)
			break
		}
	}
	p, ok := c.ready[fileID]
	delete(c.ready, fileID)
	c.mu.Unlock()
	if ok {
		os.Remove(p)
	}
	c.db().Exec(`DELETE FROM cache_entries WHERE file_id=?`, fileID)
}

// Clear empties the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	ids := make([]int64, 0, len(c.ready))
	for id := range c.ready {
		ids = append(ids, id)
	}
	c.queue = nil
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.Remove(id)
	}
}

func (c *Cache) used() int64 {
	var n int64
	c.db().QueryRow(`SELECT COALESCE(SUM(size),0) FROM cache_entries`).Scan(&n)
	return n
}

func diskSpace(dir string) (free, total int64) {
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		return 0, 0
	}
	return int64(fs.Bavail) * int64(fs.Bsize), int64(fs.Blocks) * int64(fs.Bsize)
}

func (c *Cache) Status() Status {
	cfg := c.st.Config()
	dir := c.Dir()
	free, total := diskSpace(dir)
	st := Status{Enabled: cfg.CacheEnabled, Dir: dir, UsedBytes: c.used(), MaxBytes: int64(cfg.CacheMaxGB) << 30, DiskFree: free, DiskTotal: total, Queued: []Job{}}
	c.mu.Lock()
	st.Files = len(c.ready)
	if c.current != nil {
		j := *c.current
		st.Current = &j
	}
	st.Queued = append(st.Queued, c.queue...)
	st.Hits, st.Misses, st.LastError = c.hits, c.misses, c.lastErr
	c.mu.Unlock()
	return st
}

func (c *Cache) Entries() ([]Entry, error) {
	rows, err := c.db().Query(`SELECT c.file_id, f.item_id, COALESCE(i.title,''), COALESCE(s.title,''), i.kind, i.season, i.episode, f.path, c.size, c.added_at, c.last_access, c.hits, c.reason
		FROM cache_entries c JOIN files f ON f.id=c.file_id LEFT JOIN items i ON i.id=f.item_id LEFT JOIN items s ON s.id=i.show_id
		ORDER BY c.last_access DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var show, kind string
		var season, episode int
		var src string
		if err := rows.Scan(&e.FileID, &e.ItemID, &e.Title, &show, &kind, &season, &episode, &src, &e.Size, &e.AddedAt, &e.LastAccess, &e.Hits, &e.Reason); err != nil {
			return nil, err
		}
		if kind == "episode" && show != "" {
			e.Title = fmt.Sprintf("%s · S%02dE%02d %s", show, season, episode, e.Title)
		}
		e.Name = filepath.Base(src)
		out = append(out, e)
	}
	return out, rows.Err()
}

// makeRoom evicts least-recently-used entries until need bytes fit.
func (c *Cache) makeRoom(need int64) error {
	cfg := c.st.Config()
	max := int64(cfg.CacheMaxGB) << 30
	minFree := int64(cfg.CacheMinFreeGB) << 30
	for {
		used := c.used()
		free, _ := diskSpace(c.Dir())
		if used+need <= max && free-need >= minFree {
			return nil
		}
		var id int64
		err := c.db().QueryRow(`SELECT file_id FROM cache_entries ORDER BY last_access ASC LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if used+need > max {
				return fmt.Errorf("file is larger than the cache")
			}
			return fmt.Errorf("not enough free space on the cache disk (keeping %d GB free)", cfg.CacheMinFreeGB)
		}
		if err != nil {
			return err
		}
		c.log.Infof("cache: evicting file %d to make room", id)
		c.Remove(id)
	}
}

func (c *Cache) worker() {
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			<-c.wake
			continue
		}
		job := c.queue[0]
		c.queue = c.queue[1:]
		job.Started = time.Now().Unix()
		ctx, cancel := context.WithCancel(context.Background())
		c.current, c.cancel = &job, cancel
		c.mu.Unlock()

		err := c.copy(ctx, &job)
		cancel()
		c.mu.Lock()
		c.current, c.cancel = nil, nil
		if err != nil && !errors.Is(err, context.Canceled) {
			c.lastErr = fmt.Sprintf("%s: %v", job.Name, err)
		}
		c.mu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			c.log.Warnf("cache: %s: %v", job.Name, err)
		}
	}
}

func (c *Cache) copy(ctx context.Context, job *Job) error {
	if !c.enabled() {
		return context.Canceled
	}
	f, err := c.st.File(job.FileID)
	if err != nil {
		return err
	}
	if err := c.makeRoom(f.Size); err != nil {
		return err
	}
	dir := c.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%d%s", f.ID, strings.ToLower(filepath.Ext(f.Path))))
	tmp := dst + ".part"
	src, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	start := time.Now()
	c.log.Infof("cache: copying %s (%d MB, %s)", job.Name, f.Size>>20, job.Reason)
	err = c.copyLimited(ctx, out, src, job)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		if st, e := os.Stat(tmp); e != nil || st.Size() != f.Size {
			err = fmt.Errorf("size mismatch after copy")
		}
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	now := time.Now().Unix()
	if _, err := c.db().Exec(`INSERT INTO cache_entries(file_id,path,size,src_mtime,added_at,last_access,reason) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(file_id) DO UPDATE SET path=excluded.path,size=excluded.size,src_mtime=excluded.src_mtime,
		added_at=excluded.added_at,last_access=excluded.last_access,
		reason = excluded.reason`,
		f.ID, dst, f.Size, f.Mtime, now, now, job.Reason); err != nil {
		os.Remove(dst)
		return err
	}
	c.mu.Lock()
	c.ready[f.ID] = dst
	c.mu.Unlock()
	secs := time.Since(start).Seconds()
	c.log.Infof("cache: cached %s in %.0fs (%.1f MB/s)", job.Name, secs, float64(f.Size)/1e6/max(secs, 0.001))
	return nil
}

// copyLimited copies with a simple rate limit so playback from the same disk
// keeps priority.
func (c *Cache) copyLimited(ctx context.Context, dst io.Writer, src io.Reader, job *Job) error {
	buf := make([]byte, 1<<20)
	start := time.Now()
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			elapsed := time.Since(start).Seconds()
			c.mu.Lock()
			job.Done = done
			if elapsed > 0 {
				job.Speed = float64(done) / elapsed
			}
			c.mu.Unlock()
			if limit := c.st.Config().CacheSpeedMBs; limit > 0 {
				want := float64(done) / (float64(limit) * 1e6)
				if d := want - elapsed; d > 0 {
					t := time.NewTimer(time.Duration(d * float64(time.Second)))
					select {
					case <-ctx.Done():
						t.Stop()
						return ctx.Err()
					case <-t.C:
					}
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// OnDisable drops queued work when the cache is switched off.
func (c *Cache) OnDisable() {
	c.mu.Lock()
	c.queue = nil
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
}

// Reconcile re-reads the directory (e.g. after the cache dir changed).
func (c *Cache) Reconcile() {
	c.mu.Lock()
	c.ready = map[int64]string{}
	c.mu.Unlock()
	c.reconcile()
}
