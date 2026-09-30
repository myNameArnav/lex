// Package intro finds TV intros so the player can offer "Skip Intro".
//
// Episodes of a season share their opening theme. We take a Chromaprint
// audio fingerprint of the first minutes of each episode and look for the
// longest stretch that matches between neighbouring episodes (the approach
// of Jellyfin's Intro Skipper). Chapters named "Intro"/"Opening" are used
// directly when present.
package intro

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math/bits"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"lex/internal/logx"
	"lex/internal/proc"
	"lex/internal/store"
)

// Each Chromaprint (algorithm 2) point covers 4096/11025/3 seconds.
const pointSecs = 4096.0 / 11025.0 / 3.0

const (
	maxBitDiff = 6   // bits that may differ for two points to count as equal
	maxGapSecs = 3.5 // tolerated mismatch inside an intro
)

type Segment struct {
	Kind   string  `json:"kind"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Source string  `json:"source"`
}

type Status struct {
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	Current   string `json:"current"`
	Seasons   int    `json:"seasons"`
	Done      int    `json:"done"`
	Found     int    `json:"found"`
	Missing   int    `json:"missing"`
	LastRun   int64  `json:"lastRun"`
}

type Detector struct {
	st        *store.Store
	log       *logx.Logger
	ffmpeg    string
	Available bool
	Resolve   func(*store.File) string

	mu     sync.Mutex
	status Status
	queued bool
	run    sync.Mutex
}

func New(st *store.Store, log *logx.Logger, ffmpeg string) *Detector {
	d := &Detector{st: st, log: log, ffmpeg: ffmpeg}
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-muxers").Output(); err == nil {
		d.Available = bytes.Contains(out, []byte("chromaprint"))
	}
	d.status.Available = d.Available
	return d
}

func (d *Detector) Status() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status
}

// Segments returns the stored segments for an item.
func Segments(db *sql.DB, itemID int64) []Segment {
	rows, err := db.Query(`SELECT kind,start,end,source FROM segments WHERE item_id=?`, itemID)
	if err != nil {
		return []Segment{}
	}
	defer rows.Close()
	out := []Segment{}
	for rows.Next() {
		var s Segment
		if rows.Scan(&s.Kind, &s.Start, &s.End, &s.Source) == nil {
			out = append(out, s)
		}
	}
	return out
}

// Reset forgets detection results for an item and its children so they're
// analysed again.
func (d *Detector) Reset(itemID int64) {
	db := d.st.DB()
	q := `SELECT id FROM items WHERE id=? OR parent_id=? OR show_id=?`
	db.Exec(`DELETE FROM segments WHERE item_id IN (`+q+`)`, itemID, itemID, itemID)
	db.Exec(`DELETE FROM intro_scans WHERE file_id IN (SELECT f.id FROM files f WHERE f.item_id IN (`+q+`))`, itemID, itemID, itemID)
}

// Trigger analyses pending seasons in the background.
func (d *Detector) Trigger() {
	if !d.Available || !d.st.Config().IntroDetect {
		return
	}
	d.mu.Lock()
	if d.status.Running {
		d.queued = true
		d.mu.Unlock()
		return
	}
	d.status.Running = true
	d.mu.Unlock()
	go d.Run(context.Background())
}

type episode struct {
	item    *store.Item
	file    *store.File
	scanned bool
	fp      []uint32
	fpErr   bool
	best    *Segment
}

func (d *Detector) Run(ctx context.Context) {
	d.run.Lock()
	defer d.run.Unlock()
	for {
		seasons, err := d.pendingSeasons()
		if err != nil {
			d.log.Warnf("intro: %v", err)
		}
		d.mu.Lock()
		d.status = Status{Available: true, Running: true, Seasons: len(seasons)}
		d.mu.Unlock()
		if len(seasons) > 0 {
			d.log.Infof("intro: analysing %d season(s)", len(seasons))
		}
		for _, sid := range seasons {
			if ctx.Err() != nil || !d.st.Config().IntroDetect {
				break
			}
			d.season(ctx, sid)
			d.mu.Lock()
			d.status.Done++
			d.mu.Unlock()
		}
		d.mu.Lock()
		again := d.queued
		d.queued = false
		d.status.Running = again
		d.status.Current = ""
		d.status.LastRun = time.Now().Unix()
		st := d.status
		d.mu.Unlock()
		if len(seasons) > 0 {
			d.log.Infof("intro: done; %d episode(s) with intros, %d without", st.Found, st.Missing)
		}
		if !again {
			return
		}
	}
}

func (d *Detector) pendingSeasons() ([]int64, error) {
	rows, err := d.st.DB().Query(`SELECT DISTINCT e.parent_id FROM items e JOIN files f ON f.item_id=e.id
		LEFT JOIN intro_scans s ON s.file_id=f.id WHERE e.kind='episode' AND s.file_id IS NULL AND f.probed_at>0 ORDER BY e.parent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

var reIntroChapter = regexp.MustCompile(`(?i)^\s*(intro|introduction|opening|opening credits|opening theme|op|main titles?|title sequence|theme song)\s*$`)

func chapterIntro(f *store.File) *Segment {
	if f.Info == nil {
		return nil
	}
	for i, c := range f.Info.Chapters {
		if !reIntroChapter.MatchString(c.Title) {
			continue
		}
		end := c.End
		if end <= c.Start && i+1 < len(f.Info.Chapters) {
			end = f.Info.Chapters[i+1].Start
		}
		if end-c.Start >= 5 && end-c.Start <= 240 {
			return &Segment{Kind: "intro", Start: c.Start, End: end, Source: "chapter"}
		}
	}
	return nil
}

func (d *Detector) season(ctx context.Context, seasonID int64) {
	cfg := d.st.Config()
	items, err := d.st.Children(seasonID, 0)
	if err != nil {
		return
	}
	var eps []*episode
	for _, it := range items {
		if it.Kind != "episode" {
			continue
		}
		files, err := d.st.ItemFiles(it.ID)
		if err != nil || len(files) == 0 || files[0].Info == nil {
			continue
		}
		var n int
		d.st.DB().QueryRow(`SELECT COUNT(*) FROM intro_scans WHERE file_id=?`, files[0].ID).Scan(&n)
		eps = append(eps, &episode{item: it, file: files[0], scanned: n > 0})
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].item.Episode < eps[j].item.Episode })
	if len(eps) == 0 {
		return
	}
	if show, err := d.st.Item(eps[0].item.ShowID); err == nil {
		d.mu.Lock()
		d.status.Current = fmt.Sprintf("%s · Season %d", show.Title, eps[0].item.Season)
		d.mu.Unlock()
	}
	for i, ep := range eps {
		if ep.scanned {
			continue
		}
		if seg := chapterIntro(ep.file); seg != nil {
			ep.best = seg
		} else if len(eps) > 1 {
			// Compare with the nearest neighbours until one matches.
			for _, off := range []int{1, -1, 2, -2, 3} {
				j := i + off
				if j < 0 || j >= len(eps) || ctx.Err() != nil {
					continue
				}
				a, b := d.fingerprint(ctx, ep, cfg), d.fingerprint(ctx, eps[j], cfg)
				if a == nil || b == nil {
					continue
				}
				sa, ea, sb, eb, ok := match(a, b, float64(cfg.IntroMinSecs), float64(cfg.IntroMaxSecs))
				if !ok {
					continue
				}
				ep.best = &Segment{Kind: "intro", Start: sa, End: ea, Source: "fingerprint"}
				// The neighbour gets the result too if it lacks one.
				if eps[j].best == nil && !eps[j].scanned {
					eps[j].best = &Segment{Kind: "intro", Start: sb, End: eb, Source: "fingerprint"}
				}
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
	for _, ep := range eps {
		if ep.scanned {
			continue
		}
		result := "none"
		if ep.best != nil {
			result = fmt.Sprintf("%.1f-%.1f", ep.best.Start, ep.best.End)
			d.st.DB().Exec(`INSERT INTO segments(item_id,kind,start,end,source) VALUES(?,?,?,?,?)
				ON CONFLICT(item_id,kind) DO UPDATE SET start=excluded.start,end=excluded.end,source=excluded.source`,
				ep.item.ID, ep.best.Kind, ep.best.Start, ep.best.End, ep.best.Source)
		}
		// A failed fingerprint (e.g. no audio) is still recorded as scanned
		// so we don't retry forever; "Reset intros" re-runs it.
		d.st.DB().Exec(`INSERT OR REPLACE INTO intro_scans(file_id,scanned_at,result) VALUES(?,?,?)`, ep.file.ID, time.Now().Unix(), result)
		d.mu.Lock()
		if ep.best != nil {
			d.status.Found++
		} else {
			d.status.Missing++
		}
		d.mu.Unlock()
	}
}

// fingerprint returns (and memoises) the episode's audio fingerprint.
func (d *Detector) fingerprint(ctx context.Context, ep *episode, cfg store.Config) []uint32 {
	if ep.fp != nil || ep.fpErr {
		return ep.fp
	}
	secs := float64(cfg.IntroScanSecs)
	if dur := ep.file.Info.Duration; dur > 0 && dur*0.4 < secs {
		secs = dur * 0.4
	}
	path := ep.file.Path
	if d.Resolve != nil {
		path = d.Resolve(ep.file)
	}
	audio := -1
	for _, s := range ep.file.Info.Streams {
		if s.Type == "audio" && (audio < 0 || s.Default) {
			audio = s.Index
			if s.Default {
				break
			}
		}
	}
	if audio < 0 {
		ep.fpErr = true
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, d.ffmpeg, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-t", strconv.FormatFloat(secs, 'f', 1, 64), "-i", path, "-map", "0:"+strconv.Itoa(audio), "-vn", "-sn", "-dn",
		"-ac", "1", "-f", "chromaprint", "-fp_format", "raw", "-")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		ep.fpErr = true
		return nil
	}
	// Background analysis must never compete with playback.
	proc.Nice(cmd.Process.Pid, 19)
	if err := cmd.Wait(); err != nil || out.Len() < 4*50 {
		d.log.Warnf("intro: fingerprint %s failed: %v %s", ep.item.Title, err, errb.String())
		ep.fpErr = true
		return nil
	}
	b := out.Bytes()
	fp := make([]uint32, len(b)/4)
	for i := range fp {
		fp[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	ep.fp = fp
	return fp
}

// match finds the longest shared stretch of two fingerprints and returns
// its position in each, in seconds.
func match(a, b []uint32, minSecs, maxSecs float64) (sa, ea, sb, eb float64, ok bool) {
	// Candidate alignments come from exactly equal points.
	index := make(map[uint32][]int, len(b))
	for j, v := range b {
		index[v] = append(index[v], j)
	}
	counts := map[int]int{}
	for i, v := range a {
		for _, j := range index[v] {
			counts[j-i]++
		}
	}
	type sc struct{ shift, n int }
	var shifts []sc
	for s, n := range counts {
		if n >= 2 {
			shifts = append(shifts, sc{s, n})
		}
	}
	sort.Slice(shifts, func(i, j int) bool { return shifts[i].n > shifts[j].n })
	if len(shifts) > 40 {
		shifts = shifts[:40]
	}
	gapSecs := maxGapSecs
	maxGap := int(gapSecs / pointSecs)
	bestLen := 0
	var bestStart, bestShift int
	for _, s := range shifts {
		runStart, lastMatch := -1, -1
		for i := range a {
			j := i + s.shift
			if j < 0 || j >= len(b) {
				continue
			}
			if bits.OnesCount32(a[i]^b[j]) > maxBitDiff {
				continue
			}
			if runStart >= 0 && i-lastMatch > maxGap {
				if l := lastMatch - runStart; l > bestLen {
					bestLen, bestStart, bestShift = l, runStart, s.shift
				}
				runStart = -1
			}
			if runStart < 0 {
				runStart = i
			}
			lastMatch = i
		}
		if runStart >= 0 {
			if l := lastMatch - runStart; l > bestLen {
				bestLen, bestStart, bestShift = l, runStart, s.shift
			}
		}
	}
	dur := float64(bestLen) * pointSecs
	if dur < minSecs || dur > maxSecs {
		return 0, 0, 0, 0, false
	}
	sa = float64(bestStart) * pointSecs
	ea = sa + dur
	sb = float64(bestStart+bestShift) * pointSecs
	eb = sb + dur
	// An intro that starts within the first couple of seconds starts at 0.
	if sa < 2 {
		sa = 0
	}
	if sb < 2 {
		sb = 0
	}
	return sa, ea, sb, eb, true
}
