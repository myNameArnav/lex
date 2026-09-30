// Package library scans media folders, parses names, and probes files.
package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
