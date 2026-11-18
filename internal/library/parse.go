// Package library scans media folders, parses names, and probes files.
package library

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".webm": true, ".ts": true,
	".m2ts": true, ".mts": true, ".wmv": true, ".flv": true, ".mpg": true, ".mpeg": true, ".ogv": true,
	".3gp": true, ".vob": true, ".divx": true, ".iso": false,
}

var subExts = map[string]bool{".srt": true, ".vtt": true, ".ass": true, ".ssa": true, ".sub": false}

func IsVideo(name string) bool    { return videoExts[strings.ToLower(filepath.Ext(name))] }
func IsSubtitle(name string) bool { return subExts[strings.ToLower(filepath.Ext(name))] }

// Folders whose contents are bonus material, not library items.
var skipDirs = map[string]bool{
	"extras": true, "featurettes": true, "behind the scenes": true, "deleted scenes": true, "interviews": true,
	"scenes": true, "shorts": true, "trailers": true, "other": true, "sample": true, "samples": true,
	"@eadir": true, "#recycle": true, "$recycle.bin": true, "lost+found": true, "subs": false,
}

func SkipDir(name string) bool {
	return strings.HasPrefix(name, ".") || skipDirs[strings.ToLower(name)]
}

var (
	reSite      = regexp.MustCompile(`(?i)^\s*(?:www\.)?[a-z0-9-]+\.(?:com|org|net|to|tv|me|io|xyz|cc|ws|in|se|nu|lol|mx)\s*[-–]\s*`)
	reBrackets  = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	reJunk      = regexp.MustCompile(`(?i)(?:^|[\s(\[\-])(?:2160p|1080[pi]|720p|576p|480p|4k|uhd|blu-?ray|bdrip|brrip|bdremux|remux|web-?dl|web-?rip|webrip|hdtv|hdrip|dvdrip|dvdscr|dvd|x26[45]|h\.?26[45]|hevc|avc|xvid|divx|10bit|8bit|hdr10\+?|hdr|dolby|atmos|ddp?\d|dd\+|e?ac-?3|aac\d?|dts|truehd|flac|proper|repack|extended|unrated|remastered|directors\.?cut|imax|dual|multi|hc|amzn|nf|dsnp|hmax|atvp|cr)(?:$|[\s)\]\-.])`)
	reParenJunk = regexp.MustCompile(`\(([^)]*)\)`)
	reSpaces    = regexp.MustCompile(`\s+`)

	reSxxExx   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,3})[ ._-]*e(\d{1,4})((?:[ ._-]*-?[ ._-]*e\d{1,4})*)(?:-(\d{1,4})(?:[^0-9p]|$))?`)
	reMultiEp  = regexp.MustCompile(`(?i)e(\d{1,4})`)
	reNxNN     = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])(\d{1,2})x(\d{2,3})(?:[^0-9a-z]|$)`)
	reEpisode  = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:episode|ep\.?|e)[ ._-]?(\d{1,4})(?:[^0-9]|$)`)
	reAbsolute = regexp.MustCompile(`(?:^|\s)[-–]\s*(\d{1,4})(?:v\d)?(?:\s|$|\[|\()`)
	reLeadNum  = regexp.MustCompile(`^(\d{1,3})(?:[ ._-]|$)`)
	reSeason   = regexp.MustCompile(`(?i)^(?:season|series|staffel|saison|temporada|s)[ ._-]*(\d{1,3})\b`)
	reSpecials = regexp.MustCompile(`(?i)^(specials?|extras? season|season 0+)$`)
)

// normalizeSeparators replaces dots/underscores used as word separators.
func normalizeSeparators(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if r == '.' {
			// Keep dots in abbreviations like "Mr. Robot" (dot followed by space)
			// and between digits like "5.1" is junk anyway; replace otherwise.
			if i+1 < len(rs) && rs[i+1] == ' ' {
				b.WriteRune(r)
				continue
			}
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// yearPositions returns byte offsets of standalone 19xx/20xx tokens.
func yearPositions(s string) []int {
	var out []int
	for i := 0; i+4 <= len(s); i++ {
		if (s[i] == '1' && s[i+1] == '9') || (s[i] == '2' && s[i+1] == '0') {
			if s[i+2] < '0' || s[i+2] > '9' || s[i+3] < '0' || s[i+3] > '9' {
				continue
			}
			if i > 0 && isAlnum(s[i-1]) {
				continue
			}
			if i+4 < len(s) && isAlnum(s[i+4]) {
				continue
			}
			out = append(out, i)
		}
	}
	return out
}

// CleanTitle turns a release-style file or folder name into a title and year.
func CleanTitle(name string) (string, int) {
	s := reSite.ReplaceAllString(name, "")
	s = normalizeSeparators(s)
	s = reBrackets.ReplaceAllString(s, " ")
	year := 0
	// Use the last plausible year that isn't at the very start.
	locs := yearPositions(s)
	for i := len(locs) - 1; i >= 0; i-- {
		start := locs[i]
		if strings.TrimSpace(strings.Trim(s[:start], "([ -")) == "" {
			continue
		}
		year, _ = strconv.Atoi(s[start : start+4])
		s = s[:start]
		break
	}
	if loc := reJunk.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s = s[:loc[0]]
	}
	// Drop parenthesised groups that are pure junk, keep things like "(US)".
	s = reParenJunk.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSpace(m[1 : len(m)-1])
		if inner == "" || reJunk.MatchString(" "+inner+" ") {
			return " "
		}
		return m
	})
	s = reSpaces.ReplaceAllString(s, " ")
	s = strings.Trim(s, " -–_.,+([")
	s = strings.TrimSpace(s)
	// Unbalanced parens left from truncation.
	if strings.Count(s, "(") > strings.Count(s, ")") {
		if i := strings.LastIndex(s, "("); i > 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	if s == "" {
		s = strings.TrimSpace(name)
	}
	return s, year
}

// SearchTitle strips decorations that confuse metadata searches, e.g. "(US)".
func SearchTitle(t string) string {
	t = reParenJunk.ReplaceAllString(t, " ")
	t = strings.NewReplacer("&", "and", ":", " ", " - ", " ").Replace(t)
	return strings.TrimSpace(reSpaces.ReplaceAllString(t, " "))
}

type EpisodeInfo struct {
	Season     int
	Episode    int
	EpisodeEnd int
	HasSeason  bool
	// AirDate (YYYY-MM-DD) is set for date-named episodes of daily shows
	// ("Show.2024.03.05.Guest.mkv"). Until the show's provider places them,
	// they are grouped by year (Season) and ordered by date (Episode=MMDD).
	AirDate string
}

// Air dates in names: YYYY.MM.DD, YYYY-MM-DD, YYYY_MM_DD, YYYY MM DD, and
// DD.MM.YYYY (or MM.DD.YYYY when the first number can't be a month). Both
// separators must be the same, month and day take two digits, and the date
// can't run into other letters or digits: that keeps out years next to audio
// channels ("2019.7.1"), resolutions, codec tags and bit depths ("10bit").
// Go's regexp has no backreferences, so there is a pattern per separator.
var reDateYMD, reDateDMY []*regexp.Regexp

func init() {
	for _, sep := range []string{`\.`, `-`, `_`, ` `} {
		reDateYMD = append(reDateYMD, regexp.MustCompile(`(?:^|[^0-9A-Za-z])((?:19|20)\d{2})`+sep+`(\d{2})`+sep+`(\d{2})(?:[^0-9A-Za-z]|$)`))
		reDateDMY = append(reDateDMY, regexp.MustCompile(`(?:^|[^0-9A-Za-z])(\d{2})`+sep+`(\d{2})`+sep+`((?:19|20)\d{2})(?:[^0-9A-Za-z]|$)`))
	}
}

// airDate finds an air date in a name. It returns the date as YYYY-MM-DD
// and the byte range of the date in name; the earliest date found wins.
func airDate(name string) (date string, start, end int, ok bool) {
	start = -1
	find := func(res []*regexp.Regexp, dmy bool) {
		for _, re := range res {
			for _, m := range re.FindAllStringSubmatchIndex(name, -1) {
				if start >= 0 && m[2] >= start {
					break
				}
				g := func(i int) int { n, _ := strconv.Atoi(name[m[2*i]:m[2*i+1]]); return n }
				y, mo, d := g(1), g(2), g(3)
				if dmy {
					y, mo, d = g(3), g(2), g(1)
					if mo > 12 && d <= 12 {
						mo, d = d, mo
					}
				}
				if validDate(y, mo, d) {
					date, start, end = fmt.Sprintf("%04d-%02d-%02d", y, mo, d), m[2], m[7]
					break
				}
			}
		}
	}
	find(reDateYMD, false)
	find(reDateDMY, true)
	return date, start, end, start >= 0
}

func validDate(y, m, d int) bool {
	if y < 1930 || y > time.Now().Year()+1 || m < 1 || m > 12 || d < 1 {
		return false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Day() == d && int(t.Month()) == m
}

// ParseEpisode extracts season/episode numbers from a file name.
// dirSeason is the season implied by the parent folder (-1 if none).
func ParseEpisode(base string, dirSeason int, allowAbsolute bool) (EpisodeInfo, bool) {
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if m := reSxxExx.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		ei := EpisodeInfo{Season: s, Episode: e, HasSeason: true}
		if m[3] != "" {
			for _, mm := range reMultiEp.FindAllStringSubmatch(m[3], -1) {
				n, _ := strconv.Atoi(mm[1])
				if n > ei.EpisodeEnd {
					ei.EpisodeEnd = n
				}
			}
		} else if m[4] != "" {
			n, _ := strconv.Atoi(m[4])
			if n > e && n-e < 10 {
				ei.EpisodeEnd = n
			}
		}
		if ei.EpisodeEnd <= ei.Episode {
			ei.EpisodeEnd = 0
		}
		return ei, true
	}
	if m := reNxNN.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		// Avoid resolutions like 1920x1080.
		if s > 0 && e < 500 {
			return EpisodeInfo{Season: s, Episode: e, HasSeason: true}, true
		}
	}
	if date, _, _, ok := airDate(name); ok {
		ei := EpisodeInfo{AirDate: date, HasSeason: true}
		ei.Season, _ = strconv.Atoi(date[:4])
		mo, _ := strconv.Atoi(date[5:7])
		d, _ := strconv.Atoi(date[8:])
		ei.Episode = mo*100 + d
		return ei, true
	}
	norm := normalizeSeparators(reBrackets.ReplaceAllString(name, " "))
	if m := reEpisode.FindStringSubmatch(norm); m != nil {
		e, _ := strconv.Atoi(m[1])
		s := dirSeason
		if s < 0 {
			s = 1
		}
		return EpisodeInfo{Season: s, Episode: e, HasSeason: dirSeason >= 0}, true
	}
	if allowAbsolute || dirSeason >= 0 {
		// Strip junk like 1080p before looking for a bare number.
		clean := reJunk.ReplaceAllString(norm, " ")
		if m := reAbsolute.FindStringSubmatch(clean); m != nil {
			e, _ := strconv.Atoi(m[1])
			if e > 0 && (e < 1900 || e > 2100) {
				s := dirSeason
				if s < 0 {
					s = 1
				}
				return EpisodeInfo{Season: s, Episode: e, HasSeason: dirSeason >= 0}, true
			}
		}
		if dirSeason >= 0 {
			if m := reLeadNum.FindStringSubmatch(strings.TrimSpace(clean)); m != nil {
				e, _ := strconv.Atoi(m[1])
				if e > 0 {
					return EpisodeInfo{Season: dirSeason, Episode: e, HasSeason: true}, true
				}
			}
		}
	}
	return EpisodeInfo{}, false
}

// SeasonFromDir returns the season number a folder name denotes, or -1.
func SeasonFromDir(name string) int {
	n := strings.TrimSpace(name)
	if reSpecials.MatchString(n) {
		return 0
	}
	if m := reSeason.FindStringSubmatch(n); m != nil {
		s, _ := strconv.Atoi(m[1])
		// "S01" alone is fine, but "S01E01" or "S01-S09" pack names are not season folders.
		rest := strings.TrimSpace(n[len(m[0]):])
		if rest == "" || !strings.ContainsAny(strings.ToLower(rest[:1]), "e-") {
			return s
		}
	}
	return -1
}

var reSample = regexp.MustCompile(`(?i)(^sample$|^sample[._-]|[._ -]sample$|[._-]sample[._-])`)

// IsSample reports whether a file looks like a release sample clip.
func IsSample(base string, size int64) bool {
	if size > 0 && size < 1<<20 {
		return true
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return reSample.MatchString(stem) && size < 400<<20
}

// ExtraSuffix reports whether a filename marks a Plex-style local extra
// (e.g. "Movie-trailer.mkv").
func ExtraSuffix(base string) bool {
	n := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	for _, s := range []string{"-trailer", "-featurette", "-behindthescenes", "-deleted", "-interview", "-scene", "-short", "-other", "-sample"} {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// Normalize lowercases and strips punctuation for fuzzy comparisons.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	// Plex's {edition-Director's Cut} tag names the edition outright.
	reEditionTag = regexp.MustCompile(`(?i)\{edition-([^}]+)\}`)
	reEditions   = []struct {
		re   *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`(?i)\bdirector'?s[ ._-]?cut\b`), "Director's Cut"},
		{regexp.MustCompile(`(?i)\bfinal[ ._-]?cut\b`), "Final Cut"},
		{regexp.MustCompile(`(?i)\bultimate[ ._-]?(?:cut|edition)\b`), "Ultimate Cut"},
		{regexp.MustCompile(`(?i)\btheatrical(?:[ ._-]?(?:cut|edition|version))?\b`), "Theatrical"},
		{regexp.MustCompile(`(?i)\bextended(?:[ ._-]?(?:cut|edition|version))?\b`), "Extended"},
		{regexp.MustCompile(`(?i)\bspecial[ ._-]?edition\b`), "Special Edition"},
		{regexp.MustCompile(`(?i)\bcriterion\b`), "Criterion"},
		{regexp.MustCompile(`(?i)\bunrated\b`), "Unrated"},
		{regexp.MustCompile(`(?i)\buncut\b`), "Uncut"},
		{regexp.MustCompile(`(?i)\bimax\b`), "IMAX"},
		{regexp.MustCompile(`(?i)\bremastered\b`), "Remastered"},
	}
	// cd1, disc 2, part.3, pt2: one part of a film split across files.
	rePart = regexp.MustCompile(`(?i)(?:^|[ ._\-\[(])(?:cd|dvd|disc|disk|part|pt)[ ._-]?(\d{1,2})(?:$|[ ._\-\])])`)
	reYear = regexp.MustCompile(`(?:^|[ ._\-(\[])(?:19|20)\d{2}(?:$|[ ._\-)\]])`)
)

// afterYear is the part of a file name after its release year (the whole
// name when there's none): edition and part words before the year belong to
// the title ("Uncut Gems (2019)", "Kill Bill Part 2 (2004)").
func afterYear(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if locs := reYear.FindAllStringIndex(name, -1); len(locs) > 0 {
		return name[locs[len(locs)-1][1]:]
	}
	return name
}

// Edition names the cut a video file holds ("Director's Cut", "Extended",
// …) from its file name, or "" when the name gives none. It tells versions
// of one title apart.
func Edition(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if m := reEditionTag.FindStringSubmatch(name); m != nil {
		return strings.TrimSpace(m[1])
	}
	rest := afterYear(path)
	for _, e := range reEditions {
		if e.re.MatchString(rest) {
			return e.name
		}
	}
	return ""
}

// Part is the part number of a film split across files (Movie cd1.mkv,
// Movie part 2.mkv), or 0.
func Part(path string) int {
	m := rePart.FindStringSubmatch(afterYear(path))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	if n < 1 {
		return 0
	}
	return n
}
