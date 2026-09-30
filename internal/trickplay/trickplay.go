// Package trickplay generates seek-bar preview thumbnails ("trickplay"):
// sprite sheets of small frames taken every few seconds.
package trickplay

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lex/internal/logx"
	"lex/internal/proc"
	"lex/internal/store"
)

const (
	cols = 10
	rows = 10
)

type Meta struct {
	Interval int `json:"interval"` // seconds per thumbnail
	Width    int `json:"width"`
	Height   int `json:"height"`
	Cols     int `json:"cols"`
	Rows     int `json:"rows"`
	Count    int `json:"count"`
	Sheets   int `json:"sheets"`
}

type Status struct {
	Enabled bool   `json:"enabled"`
	Running bool   `json:"running"`
	Paused  bool   `json:"paused"` // waiting for playback to finish
	Current string `json:"current"`
	Done    int    `json:"done"`
	Pending int    `json:"pending"`
	Failed  int    `json:"failed"`
}

type Generator struct {
	st     *store.Store
	log    *logx.Logger
	ffmpeg string
	dir    string

	Resolve  func(*store.File) string
	Busy     func() bool // playback in progress: don't compete for the disk
	HWDecode func() bool

	mu       sync.Mutex
	status   Status
	priority []int64
	wake     chan struct{}
	failed   map[int64]time.Time
}

func New(st *store.Store, log *logx.Logger, ffmpeg, dir string) *Generator {
	os.MkdirAll(dir, 0o755)
	g := &Generator{st: st, log: log, ffmpeg: ffmpeg, dir: dir, wake: make(chan struct{}, 1), failed: map[int64]time.Time{}}
	go g.loop()
	return g
}

func (g *Generator) fileDir(f *store.File) string {
	return filepath.Join(g.dir, fmt.Sprintf("%d-%d", f.ID, f.Mtime))
}

// Meta returns the previews for a file, if generated.
func (g *Generator) Meta(f *store.File) (*Meta, string, error) {
	dir := g.fileDir(f)
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, "", err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "", err
	}
	return &m, dir, nil
}

func (g *Generator) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.status
	s.Enabled = g.st.Config().TrickplayEnabled
	return s
}

// Trigger wakes the worker (e.g. after a library scan).
func (g *Generator) Trigger() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

// Prioritize moves a file to the front of the queue (it's being watched).
func (g *Generator) Prioritize(fileID int64) {
	g.mu.Lock()
	g.priority = append([]int64{fileID}, g.priority...)
	g.mu.Unlock()
	g.Trigger()
}

// pending returns files that still need previews, prioritised ones first.
func (g *Generator) pending() []*store.File {
	files, err := g.st.AllFiles()
	if err != nil {
		return nil
	}
	var out []*store.File
	for _, f := range files {
		if f.Info == nil || f.Duration < 60 || f.Info.Video() == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(g.fileDir(f), "meta.json")); err == nil {
			continue
		}
		if t, ok := g.failed[f.ID]; ok && time.Since(t) < 24*time.Hour {
			continue
		}
		out = append(out, f)
	}
	g.mu.Lock()
	prio := map[int64]int{}
	for i, id := range g.priority {
		if _, ok := prio[id]; !ok {
			prio[id] = i
		}
	}
	g.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		pi, iok := prio[out[i].ID]
		pj, jok := prio[out[j].ID]
		if iok != jok {
			return iok
		}
		if iok {
			return pi < pj
		}
		return out[i].AddedAt > out[j].AddedAt
	})
	return out
}

func (g *Generator) loop() {
	time.Sleep(30 * time.Second) // let startup scans settle
	for {
		if !g.st.Config().TrickplayEnabled {
			g.waitWake(10 * time.Minute)
			continue
		}
		files := g.pending()
		g.mu.Lock()
		g.status.Pending = len(files)
		g.mu.Unlock()
		if len(files) == 0 {
			g.waitWake(30 * time.Minute)
			continue
		}
		var f *store.File
		for _, c := range files {
			if g.canRun(c) {
				f = c
				break
			}
		}
		if f == nil {
			g.set(func(s *Status) { s.Paused = true })
			time.Sleep(20 * time.Second)
			continue
		}
		g.set(func(s *Status) { s.Paused = false; s.Running = true; s.Current = filepath.Base(f.Path) })
		start := time.Now()
		err := g.generate(f)
		g.mu.Lock()
		g.status.Running, g.status.Current = false, ""
		if err == errPaused {
			g.mu.Unlock()
			continue
		}
		if err != nil {
			g.failed[f.ID] = time.Now()
			g.status.Failed++
		} else {
			g.status.Done++
		}
		g.priority = removeID(g.priority, f.ID)
		g.mu.Unlock()
		if err != nil {
			g.log.Warnf("trickplay %s: %v", filepath.Base(f.Path), err)
		} else {
			g.log.Debugf("trickplay %s in %s", filepath.Base(f.Path), time.Since(start).Round(time.Second))
		}
	}
}

// canRun: while something is playing, only work on files read from the SSD
// cache, so the hard drive stays free for playback.
func (g *Generator) canRun(f *store.File) bool {
	if g.Busy == nil || !g.Busy() {
		return true
	}
	return g.Resolve != nil && g.Resolve(f) != f.Path
}

func removeID(ids []int64, id int64) []int64 {
	out := ids[:0]
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

func (g *Generator) set(fn func(*Status)) {
	g.mu.Lock()
	fn(&g.status)
	g.mu.Unlock()
}

func (g *Generator) waitWake(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-g.wake:
	case <-t.C:
	}
}

func (g *Generator) generate(f *store.File) error {
	cfg := g.st.Config()
	v := f.Info.Video()
	interval, width := cfg.TrickplayInterval, cfg.TrickplayWidth
	height := width * 9 / 16
	if v.Width > 0 && v.Height > 0 {
		height = int(math.Round(float64(width)*float64(v.Height)/float64(v.Width)/2)) * 2
	}
	count := int(math.Ceil(f.Duration / float64(interval)))
	dir := g.fileDir(f)
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	src := f.Path
	if g.Resolve != nil {
		src = g.Resolve(f)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if v.Codec == "hevc" && g.HWDecode != nil && g.HWDecode() {
		args = append(args, "-hwaccel", "drm")
	}
	// Decoding only keyframes keeps this cheap; fps= then picks one frame per
	// interval from them.
	args = append(args, "-skip_frame", "nokey", "-i", src, "-an", "-sn", "-dn",
		"-vf", fmt.Sprintf("fps=1/%d,scale=%d:%d:flags=fast_bilinear,tile=%dx%d", interval, width, height, cols, rows),
		"-q:v", "7", "-f", "image2", filepath.Join(tmp, "%d.jpg"))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.ffmpeg, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	proc.Nice(cmd.Process.Pid, 19)
	// Stop early if someone starts watching; we'll redo this file later.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			if err != nil {
				os.RemoveAll(tmp)
				return fmt.Errorf("%v %s", err, strings.TrimSpace(stderr.String()))
			}
			sheets := 0
			ents, _ := os.ReadDir(tmp)
			for _, e := range ents {
				if strings.HasSuffix(e.Name(), ".jpg") {
					sheets++
				}
			}
			if sheets == 0 {
				os.RemoveAll(tmp)
				return fmt.Errorf("no thumbnails produced")
			}
			if c := sheets * cols * rows; count > c {
				count = c
			}
			m := Meta{Interval: interval, Width: width, Height: height, Cols: cols, Rows: rows, Count: count, Sheets: sheets}
			b, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(tmp, "meta.json"), b, 0o644); err != nil {
				return err
			}
			os.RemoveAll(dir)
			return os.Rename(tmp, dir)
		case <-time.After(5 * time.Second):
			if !g.canRun(f) {
				cmd.Process.Kill()
				<-done
				os.RemoveAll(tmp)
				g.set(func(s *Status) { s.Paused = true })
				return errPaused
			}
		}
	}
}

var errPaused = fmt.Errorf("paused for playback")

// Sheet returns the path of sheet n (1-based) for a file.
func Sheet(dir string, n int) string {
	return filepath.Join(dir, strconv.Itoa(n)+".jpg")
}
