package store

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func (s *Store) extBuckets() ([]Bucket, error) {
	rows, err := s.db.Query(`SELECT path,size FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]*Bucket{}
	for rows.Next() {
		var p string
		var size int64
		if err := rows.Scan(&p, &size); err != nil {
			return nil, err
		}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(p)), ".")
		b := m[ext]
		if b == nil {
			b = &Bucket{Key: ext}
			m[ext] = b
		}
		b.Count++
		b.Value += float64(size)
	}
	out := make([]Bucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, rows.Err()
}

type Bucket struct {
	Key   string  `json:"key"`
	Count int     `json:"count"`
	Value float64 `json:"value"` // bytes, hours, etc. depending on context
}

func (s *Store) buckets(q string, args ...any) ([]Bucket, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Count, &b.Value); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

type LibraryStats struct {
	Movies        int      `json:"movies"`
	Shows         int      `json:"shows"`
	Seasons       int      `json:"seasons"`
	Episodes      int      `json:"episodes"`
	Files         int      `json:"files"`
	TotalBytes    int64    `json:"totalBytes"`
	TotalDuration float64  `json:"totalDuration"`
	Unprobed      int      `json:"unprobed"`
	ProbeErrors   int      `json:"probeErrors"`
	MetaMatched   int      `json:"metaMatched"`
	MetaMissing   int      `json:"metaMissing"`
	MetaPending   int      `json:"metaPending"`
	VideoCodecs   []Bucket `json:"videoCodecs"`
	AudioCodecs   []Bucket `json:"audioCodecs"`
	Containers    []Bucket `json:"containers"`
	Resolutions   []Bucket `json:"resolutions"`
	HDR           []Bucket `json:"hdr"`
	Libraries     []Bucket `json:"libraries"`
}

func (s *Store) LibraryStats() (*LibraryStats, error) {
	st := &LibraryStats{}
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM items WHERE kind='movie'),
		(SELECT COUNT(*) FROM items WHERE kind='show'),
		(SELECT COUNT(*) FROM items WHERE kind='season'),
		(SELECT COUNT(*) FROM items WHERE kind='episode'),
		(SELECT COUNT(*) FROM files),
		(SELECT COALESCE(SUM(size),0) FROM files),
		(SELECT COALESCE(SUM(duration),0) FROM files),
		(SELECT COUNT(*) FROM files WHERE probed_at=0),
		(SELECT COUNT(*) FROM files WHERE probe_error!=''),
		(SELECT COUNT(*) FROM items WHERE kind IN ('movie','show') AND meta_status IN (1,3)),
		(SELECT COUNT(*) FROM items WHERE kind IN ('movie','show') AND meta_status=2),
		(SELECT COUNT(*) FROM items WHERE meta_status=0)`).Scan(
		&st.Movies, &st.Shows, &st.Seasons, &st.Episodes, &st.Files, &st.TotalBytes, &st.TotalDuration,
		&st.Unprobed, &st.ProbeErrors, &st.MetaMatched, &st.MetaMissing, &st.MetaPending)
	if err != nil {
		return nil, err
	}
	if st.VideoCodecs, err = s.buckets(`SELECT CASE vcodec WHEN '' THEN 'unknown' ELSE vcodec END, COUNT(*), SUM(size) FROM files GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return nil, err
	}
	// Any audio track counts (a file with English AAC and a DTS track is in
	// both), so these add up to more than the number of files.
	if st.AudioCodecs, err = s.buckets(`SELECT c, COUNT(*), SUM(size) FROM (
		SELECT DISTINCT f.id, f.size, COALESCE(NULLIF(json_extract(t.value,'$.codec'),''),'unknown') c
		FROM files f, json_each(f.info,'$.streams') t WHERE json_valid(f.info) AND json_extract(t.value,'$.type')='audio')
		GROUP BY c ORDER BY 2 DESC`); err != nil {
		return nil, err
	}
	if st.Containers, err = s.extBuckets(); err != nil {
		return nil, err
	}
	if st.Resolutions, err = s.buckets(`SELECT ` + resolutionOf + ` r, COUNT(*), SUM(size) FROM files f GROUP BY r ORDER BY 2 DESC`); err != nil {
		return nil, err
	}
	if st.HDR, err = s.buckets(`SELECT CASE hdr WHEN '' THEN 'SDR' ELSE hdr END, COUNT(*), SUM(size) FROM files GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return nil, err
	}
	if st.Libraries, err = s.buckets(`SELECT l.name, COUNT(f.id), COALESCE(SUM(f.size),0) FROM libraries l LEFT JOIN files f ON f.library_id=l.id GROUP BY l.id ORDER BY l.id`); err != nil {
		return nil, err
	}
	return st, nil
}

const resolutionOf = `CASE WHEN f.width>=3200 OR f.height>=2000 THEN '4K' WHEN f.width>=1800 OR f.height>=1000 THEN '1080p' WHEN f.width>=1200 OR f.height>=700 THEN '720p' WHEN f.width>0 THEN 'SD' ELSE 'unknown' END`

// LibraryTitle is a movie, or a show with its matching episodes, in a
// Library-stats category.
type LibraryTitle struct {
	ItemID int64  `json:"itemId"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// libraryDims are the Library-stats categories: each maps a bucket key to
// the files in it, using the same definitions as LibraryStats.
var libraryDims = map[string]string{
	"library":    `(SELECT name FROM libraries WHERE id=f.library_id) = ?`,
	"video":      `(CASE f.vcodec WHEN '' THEN 'unknown' ELSE f.vcodec END) = ?`,
	"audio":      `EXISTS (SELECT 1 FROM json_each(f.info,'$.streams') t WHERE json_valid(f.info) AND json_extract(t.value,'$.type')='audio' AND COALESCE(NULLIF(json_extract(t.value,'$.codec'),''),'unknown') = ?)`,
	"hdr":        `(CASE f.hdr WHEN '' THEN 'SDR' ELSE f.hdr END) = ?`,
	"resolution": resolutionOf + ` = ?`,
	"container":  `lower(f.path) LIKE '%.' || ? ESCAPE '\'`,
}

// LibraryTitles lists the titles with files in one Library-stats category,
// largest first; episodes are grouped under their show.
func (s *Store) LibraryTitles(dim, key string) ([]LibraryTitle, error) {
	cond, ok := libraryDims[dim]
	if !ok {
		return nil, fmt.Errorf("unknown category %q", dim)
	}
	arg := key
	if dim == "container" {
		arg = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(key))
	}
	rows, err := s.db.Query(`SELECT g.id, g.kind, g.title, g.year, x.n, x.b FROM (
		SELECT CASE WHEN i.kind='episode' AND i.show_id>0 THEN i.show_id ELSE i.id END gid, COUNT(*) n, SUM(f.size) b
		FROM files f JOIN items i ON i.id=f.item_id WHERE `+cond+` GROUP BY gid) x
		JOIN items g ON g.id=x.gid ORDER BY x.b DESC LIMIT 500`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryTitle{}
	for rows.Next() {
		var t LibraryTitle
		if err := rows.Scan(&t.ItemID, &t.Kind, &t.Title, &t.Year, &t.Files, &t.Bytes); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type DayStat struct {
	Day   string  `json:"day"`
	Plays int     `json:"plays"`
	Hours float64 `json:"hours"`
	Bytes int64   `json:"bytes"`
}

type PlaybackStats struct {
	Plays          int       `json:"plays"`
	Hours          float64   `json:"hours"`
	Bytes          int64     `json:"bytes"`
	UniqueItems    int       `json:"uniqueItems"`
	RemotePlays    int       `json:"remotePlays"`
	BufferEvents   int       `json:"bufferEvents"`
	BufferSeconds  float64   `json:"bufferSeconds"`
	Methods        []Bucket  `json:"methods"`
	Users          []Bucket  `json:"users"`
	Clients        []Bucket  `json:"clients"`
	TopItems       []Bucket  `json:"topItems"`
	Reasons        []Bucket  `json:"reasons"`
	BufferByMethod []Bucket  `json:"bufferByMethod"`
	Days           []DayStat `json:"days"`
	Hours24        []Bucket  `json:"hoursOfDay"`
}

// titleKey groups history rows by title: episodes count toward their show.
const titleKey = `(SELECT CASE WHEN i.show_id > 0 THEN 's' || i.show_id ELSE 'i' || i.id END FROM items i WHERE i.id = history.item_id)`

// PlaybackStats summarises the last days of history. tzMinutes is the
// viewer's offset east of UTC, so days and hours of day are their local ones
// (the server often runs in UTC, e.g. in a container).
func (s *Store) PlaybackStats(days, tzMinutes int) (*PlaybackStats, error) {
	since := time.Now().AddDate(0, 0, -days).Unix()
	tz := fmt.Sprintf("%+d minutes", tzMinutes)
	st := &PlaybackStats{}
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(watched),0)/3600.0,COALESCE(SUM(bytes),0),COUNT(DISTINCT COALESCE(`+titleKey+`, 'i' || item_id)),COALESCE(SUM(remote),0),
		COALESCE(SUM(buffer_events),0),COALESCE(SUM(buffer_seconds),0) FROM history WHERE started_at>=?`, since).
		Scan(&st.Plays, &st.Hours, &st.Bytes, &st.UniqueItems, &st.RemotePlays, &st.BufferEvents, &st.BufferSeconds)
	if err != nil {
		return nil, err
	}
	if st.Methods, err = s.buckets(`SELECT method, COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? GROUP BY method ORDER BY 2 DESC`, since); err != nil {
		return nil, err
	}
	if st.Users, err = s.buckets(`SELECT user_name, COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? GROUP BY user_id ORDER BY 3 DESC LIMIT 20`, since); err != nil {
		return nil, err
	}
	if st.Clients, err = s.buckets(`SELECT client, COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? GROUP BY client ORDER BY 2 DESC LIMIT 20`, since); err != nil {
		return nil, err
	}
	if st.TopItems, err = s.buckets(`SELECT MAX(title), COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? GROUP BY COALESCE(`+titleKey+`, 'i' || item_id) ORDER BY 2 DESC, 3 DESC LIMIT 15`, since); err != nil {
		return nil, err
	}
	if st.Reasons, err = s.reasonBuckets(since); err != nil {
		return nil, err
	}
	if st.BufferByMethod, err = s.buckets(`SELECT method, SUM(buffer_events), CASE WHEN SUM(watched)>0 THEN SUM(buffer_seconds)*100.0/SUM(watched) ELSE 0 END FROM history WHERE started_at>=? GROUP BY method`, since); err != nil {
		return nil, err
	}
	if st.Hours24, err = s.buckets(`SELECT strftime('%H', started_at, 'unixepoch', ?), COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? GROUP BY 1 ORDER BY 1`, tz, since); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT date(started_at,'unixepoch',?) d, COUNT(*), SUM(watched)/3600.0, SUM(bytes) FROM history WHERE started_at>=? GROUP BY d ORDER BY d`, tz, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d DayStat
		if err := rows.Scan(&d.Day, &d.Plays, &d.Hours, &d.Bytes); err != nil {
			return nil, err
		}
		st.Days = append(st.Days, d)
	}
	return st, rows.Err()
}

// bitrateReason matches decide.go's "bitrate 12000 kbps over 8000 kbps limit".
var bitrateReason = regexp.MustCompile(`^bitrate \d+ kbps over \d+ kbps limit$`)

// reasonBuckets counts each conversion reason on its own: history keeps a
// play's reasons joined with "; ", and a play counts once toward each of its
// reasons. Numbers are dropped so bitrate reasons group together.
func (s *Store) reasonBuckets(since int64) ([]Bucket, error) {
	rows, err := s.buckets(`SELECT reasons, COUNT(*), SUM(watched)/3600.0 FROM history WHERE started_at>=? AND reasons!='' GROUP BY reasons`, since)
	if err != nil {
		return nil, err
	}
	m := map[string]*Bucket{}
	for _, r := range rows {
		seen := map[string]bool{}
		for _, k := range strings.Split(r.Key, "; ") {
			k = strings.TrimSpace(k)
			if bitrateReason.MatchString(k) {
				k = "bitrate over the limit"
			}
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			b := m[k]
			if b == nil {
				b = &Bucket{Key: k}
				m[k] = b
			}
			b.Count += r.Count
			b.Value += r.Value
		}
	}
	out := make([]Bucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > 15 {
		out = out[:15]
	}
	return out, nil
}
