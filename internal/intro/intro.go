// Package intro finds TV intros and end credits so the player can offer
// "Skip Intro" and start "Up next" when the credits roll.
//
// Episodes of a season share their opening theme. We take a Chromaprint
// audio fingerprint of the first minutes of each episode and look for the
// longest stretch that matches between neighbouring episodes (the approach
// of Jellyfin's Intro Skipper). End credits work the same way on the last
// minutes. Chapters named "Intro"/"Opening" or "Credits"/"Ending" are used
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
	// End credits, counted separately: a season can have one without the other.
	CreditsFound   int   `json:"creditsFound"`
	CreditsMissing int   `json:"creditsMissing"`
	LastRun        int64 `json:"lastRun"`
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
	for _, t := range []string{"intro_scans", "credits_scans"} {
		db.Exec(`DELETE FROM `+t+` WHERE file_id IN (SELECT f.id FROM files f WHERE f.item_id IN (`+q+`))`, itemID, itemID, itemID)
	}
}

// enabled reports whether either kind of detection is switched on.
func enabled(cfg store.Config) bool { return cfg.IntroDetect || cfg.CreditsDetect }

// Trigger analyses pending seasons in the background.
func (d *Detector) Trigger() {
	if !d.Available || !enabled(d.st.Config()) {
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
	item *store.Item
	file *store.File
	// What's already stored (or switched off), so it isn't analysed again.
	introDone, creditsDone bool
	head, tail             fingerprint // memoised fingerprints of the start and end
	intro, credits         *Segment
}

// fingerprint is one memoised Chromaprint run. off is where it starts in
// the episode, in seconds.
type fingerprint struct {
	fp   []uint32
	off  float64
	done bool
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
			if ctx.Err() != nil || !enabled(d.st.Config()) {
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
			d.log.Infof("intro: done; %d episode(s) with intros, %d without; %d with end credits, %d without",
				st.Found, st.Missing, st.CreditsFound, st.CreditsMissing)
		}
		if !again {
			return
		}
	}
}

func (d *Detector) pendingSeasons() ([]int64, error) {
	cfg := d.st.Config()
	rows, err := d.st.DB().Query(`SELECT DISTINCT e.parent_id FROM items e JOIN files f ON f.item_id=e.id
		LEFT JOIN intro_scans s ON s.file_id=f.id LEFT JOIN credits_scans c ON c.file_id=f.id
		WHERE e.kind='episode' AND f.probed_at>0 AND ((? AND s.file_id IS NULL) OR (? AND c.file_id IS NULL))
		ORDER BY e.parent_id`, cfg.IntroDetect, cfg.CreditsDetect)
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

var reCreditsChapter = regexp.MustCompile(`(?i)^\s*(credits|end credits|ending|ending credits|ending theme|closing credits|closing|outro|ed)\s*$`)

func chapterIntro(f *store.File) *Segment {
	return chapterSegment(f, "intro", reIntroChapter, 5, 240)
}

// CreditsStart is where an item's end credits start for file f: the
// detected (or stored chapter) segment, else a Credits chapter of f, else 0.
func CreditsStart(db *sql.DB, itemID int64, f *store.File) float64 {
	for _, s := range Segments(db, itemID) {
		if s.Kind == "credits" && s.Start > 0 {
			return s.Start
		}
	}
	if f != nil {
		if seg := chapterCredits(f); seg != nil {
			return seg.Start
		}
	}
	return 0
}

// chapterCredits only accepts a chapter in the last third, so a chapter
// named "Ending" in the middle of a film isn't taken for the credits.
func chapterCredits(f *store.File) *Segment {
	seg := chapterSegment(f, "credits", reCreditsChapter, 5, 900)
	if seg == nil || f.Info.Duration <= 0 || seg.Start < f.Info.Duration*2/3 {
		return nil
	}
	return seg
}

func chapterSegment(f *store.File, kind string, re *regexp.Regexp, minLen, maxLen float64) *Segment {
	if f.Info == nil {
		return nil
	}
	for i, c := range f.Info.Chapters {
		if !re.MatchString(c.Title) {
			continue
		}
		end := c.End
		if end <= c.Start {
			if i+1 < len(f.Info.Chapters) {
				end = f.Info.Chapters[i+1].Start
			} else {
				end = f.Info.Duration
			}
		}
		if end-c.Start >= minLen && end-c.Start <= maxLen {
			return &Segment{Kind: kind, Start: c.Start, End: end, Source: "chapter"}
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
	scanned := func(table string, fileID int64) bool {
		var n int
		d.st.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE file_id=?`, fileID).Scan(&n)
		return n > 0
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
		eps = append(eps, &episode{item: it, file: files[0],
			introDone:   !cfg.IntroDetect || scanned("intro_scans", files[0].ID),
			creditsDone: !cfg.CreditsDetect || scanned("credits_scans", files[0].ID)})
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

	introMin, introMax := float64(cfg.IntroMinSecs), float64(cfg.IntroMaxSecs)
	d.pass(ctx, eps, kindPass{
		done:    func(e *episode) bool { return e.introDone },
		result:  func(e *episode) **Segment { return &e.intro },
		chapter: chapterIntro,
		fp:      func(e *episode) []uint32 { return d.head(ctx, e, cfg).fp },
		match: func(a, b *episode) (*Segment, *Segment) {
			sa, ea, sb, eb, ok := match(a.head.fp, b.head.fp, introMin, introMax)
			if !ok {
				return nil, nil
			}
			return &Segment{Kind: "intro", Start: sa, End: ea, Source: "fingerprint"},
				&Segment{Kind: "intro", Start: sb, End: eb, Source: "fingerprint"}
		},
	})
	creditsMin := float64(cfg.CreditsMinSecs)
	d.pass(ctx, eps, kindPass{
		done:    func(e *episode) bool { return e.creditsDone },
		result:  func(e *episode) **Segment { return &e.credits },
		chapter: chapterCredits,
		fp:      func(e *episode) []uint32 { return d.tail(ctx, e, cfg).fp },
		match: func(a, b *episode) (*Segment, *Segment) {
			sa, ea, sb, eb, ok := matchTail(a.tail.fp, b.tail.fp, a.tail.off, b.tail.off,
				a.file.Info.Duration, b.file.Info.Duration, creditsMin)
			if !ok {
				return nil, nil
			}
			return &Segment{Kind: "credits", Start: sa, End: ea, Source: "fingerprint"},
				&Segment{Kind: "credits", Start: sb, End: eb, Source: "fingerprint"}
		},
	})
	if ctx.Err() != nil {
		return
	}

	for _, ep := range eps {
		if !ep.introDone {
			d.save(ep, "intro_scans", ep.intro)
		}
		if !ep.creditsDone {
			d.save(ep, "credits_scans", ep.credits)
		}
		d.mu.Lock()
		if !ep.introDone {
			if ep.intro != nil {
				d.status.Found++
			} else {
				d.status.Missing++
			}
		}
		if !ep.creditsDone {
			if ep.credits != nil {
				d.status.CreditsFound++
			} else {
				d.status.CreditsMissing++
			}
		}
		d.mu.Unlock()
	}
}

// kindPass is what differs between finding intros and finding credits.
type kindPass struct {
	done    func(*episode) bool
	result  func(*episode) **Segment
	chapter func(*store.File) *Segment
	fp      func(*episode) []uint32
	match   func(a, b *episode) (*Segment, *Segment)
}

// pass finds one kind of segment for each episode that needs it: from a
// chapter, or by comparing with the nearest neighbours until one matches.
func (d *Detector) pass(ctx context.Context, eps []*episode, k kindPass) {
	for i, ep := range eps {
		if k.done(ep) || ctx.Err() != nil {
			continue
		}
		res := k.result(ep)
		if seg := k.chapter(ep.file); seg != nil {
			*res = seg
			continue
		}
		if *res != nil || len(eps) < 2 {
			continue
		}
		for _, off := range []int{1, -1, 2, -2, 3} {
			j := i + off
			if j < 0 || j >= len(eps) || ctx.Err() != nil {
				continue
			}
			if k.fp(ep) == nil || k.fp(eps[j]) == nil {
				continue
			}
			a, b := k.match(ep, eps[j])
			if a == nil {
				continue
			}
			*res = a
			// The neighbour gets the result too if it lacks one.
			if other := k.result(eps[j]); *other == nil && !k.done(eps[j]) {
				*other = b
			}
			break
		}
	}
}

// save stores a found segment and records the file as analysed in table.
// A failed fingerprint (e.g. no audio) is still recorded as scanned so we
// don't retry forever; "Re-detect" re-runs it.
func (d *Detector) save(ep *episode, table string, seg *Segment) {
	result := "none"
	if seg != nil {
		result = fmt.Sprintf("%.1f-%.1f", seg.Start, seg.End)
		d.st.DB().Exec(`INSERT INTO segments(item_id,kind,start,end,source) VALUES(?,?,?,?,?)
			ON CONFLICT(item_id,kind) DO UPDATE SET start=excluded.start,end=excluded.end,source=excluded.source`,
			ep.item.ID, seg.Kind, seg.Start, seg.End, seg.Source)
	}
	d.st.DB().Exec(`INSERT OR REPLACE INTO `+table+`(file_id,scanned_at,result) VALUES(?,?,?)`, ep.file.ID, time.Now().Unix(), result)
}

// head fingerprints the start of the episode, where the intro is.
func (d *Detector) head(ctx context.Context, ep *episode, cfg store.Config) *fingerprint {
	if !ep.head.done {
		ep.head.done = true
		secs := float64(cfg.IntroScanSecs)
		if dur := ep.file.Info.Duration; dur > 0 && dur*0.4 < secs {
			secs = dur * 0.4
		}
		ep.head.fp = d.chromaprint(ctx, ep, 0, secs)
	}
	return &ep.head
}

// tail fingerprints the end of the episode, where the credits are. It
// stays clear of the part head covers so an intro is never taken for
// credits.
func (d *Detector) tail(ctx context.Context, ep *episode, cfg store.Config) *fingerprint {
	if !ep.tail.done {
		ep.tail.done = true
		dur := ep.file.Info.Duration
		if dur <= 0 {
			return &ep.tail
		}
		secs := min(float64(cfg.CreditsScanSecs), dur*0.4)
		ep.tail.off = dur - secs
		ep.tail.fp = d.chromaprint(ctx, ep, ep.tail.off, secs)
	}
	return &ep.tail
}

// chromaprint returns the raw fingerprint of secs of the episode's main
// audio track starting at from, or nil if that fails.
func (d *Detector) chromaprint(ctx context.Context, ep *episode, from, secs float64) []uint32 {
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
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if from > 0 {
		args = append(args, "-ss", strconv.FormatFloat(from, 'f', 3, 64))
	}
	args = append(args, "-t", strconv.FormatFloat(secs, 'f', 1, 64), "-i", path, "-map", "0:"+strconv.Itoa(audio), "-vn", "-sn", "-dn",
		"-ac", "1", "-f", "chromaprint", "-fp_format", "raw", "-")
	cmd := exec.CommandContext(cctx, d.ffmpeg, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		return nil
	}
	// Background analysis must never compete with playback.
	proc.Nice(cmd.Process.Pid, 19)
	if err := cmd.Wait(); err != nil || out.Len() < 4*50 {
		d.log.Warnf("intro: fingerprint %s at %.0fs failed: %v %s", ep.item.Title, from, err, errb.String())
		return nil
	}
	b := out.Bytes()
	fp := make([]uint32, len(b)/4)
	for i := range fp {
		fp[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	return fp
}

// match finds the longest shared stretch of two fingerprints and returns
// its position in each, in seconds.
func match(a, b []uint32, minSecs, maxSecs float64) (sa, ea, sb, eb float64, ok bool) {
	start, shift, n := longestRun(a, b)
	dur := float64(n) * pointSecs
	if dur < minSecs || dur > maxSecs {
		return 0, 0, 0, 0, false
	}
	sa = float64(start) * pointSecs
	ea = sa + dur
	sb = float64(start+shift) * pointSecs
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

// tailSnapSecs: credits that stop this close to the end run to the end
// (the last seconds are often silence or a fade the fingerprint drops).
const tailSnapSecs = 5.0

// matchTail finds end credits shared by two fingerprints of the ends of
// episodes. offA/offB are where each fingerprint starts in its episode and
// durA/durB the episodes' lengths; the result is in episode time. Credits
// can be as long as the analysed window, so there's no upper bound.
func matchTail(a, b []uint32, offA, offB, durA, durB, minSecs float64) (sa, ea, sb, eb float64, ok bool) {
	start, shift, n := longestRun(a, b)
	dur := float64(n) * pointSecs
	if dur < minSecs {
		return 0, 0, 0, 0, false
	}
	sa = offA + float64(start)*pointSecs
	sb = offB + float64(start+shift)*pointSecs
	ea, eb = sa+dur, sb+dur
	if durA > 0 && durA-ea < tailSnapSecs {
		ea = durA
	}
	if durB > 0 && durB-eb < tailSnapSecs {
		eb = durB
	}
	return sa, ea, sb, eb, true
}

// longestRun finds the longest stretch of a that matches b, tolerating
// short gaps. It returns where it starts in a, the offset to the same
// point in b (j = i + shift) and its length, all in points.
func longestRun(a, b []uint32) (start, shift, n int) {
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
	return bestStart, bestShift, bestLen
}
