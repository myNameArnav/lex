package stream

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AssToVTT converts an ASS/SSA script into plain WebVTT dialogue, for
// browsers that can't run the libass renderer. Typesetting (signs, drawings,
// karaoke) is dropped: rendered as plain text at the bottom of the screen it
// would bury the dialogue. ffmpeg's converter keeps all of it and is far too
// slow on a Pi for heavily typeset anime releases.
func AssToVTT(ass []byte) []byte {
	type cue struct {
		start, end float64
		text       string
	}
	var cues []cue
	var format []string
	inEvents := false
	sc := bufio.NewScanner(bytes.NewReader(ass))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if strings.HasPrefix(line, "[") {
			inEvents = strings.EqualFold(line, "[Events]")
			continue
		}
		if !inEvents {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Format":
			format = nil
			for _, f := range strings.Split(val, ",") {
				format = append(format, strings.ToLower(strings.TrimSpace(f)))
			}
		case "Dialogue":
			if len(format) == 0 {
				format = []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
			}
			fields := strings.SplitN(strings.TrimSpace(val), ",", len(format))
			if len(fields) != len(format) {
				continue
			}
			ev := map[string]string{}
			for i, f := range format {
				ev[f] = fields[i]
			}
			if isTypesetting(ev["style"], ev["effect"], ev["text"]) {
				continue
			}
			text := assText(ev["text"])
			start, e1 := parseASSTime(ev["start"])
			end, e2 := parseASSTime(ev["end"])
			if text == "" || e1 != nil || e2 != nil || end <= start {
				continue
			}
			cues = append(cues, cue{start, end, text})
		}
	}
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].start < cues[j].start })
	// Merge repeats of the same line (split into many short events for
	// effects) into one cue.
	var merged []cue
	for _, c := range cues {
		if n := len(merged); n > 0 && merged[n-1].text == c.text && c.start <= merged[n-1].end+0.1 {
			if c.end > merged[n-1].end {
				merged[n-1].end = c.end
			}
			continue
		}
		merged = append(merged, c)
	}
	var b bytes.Buffer
	b.WriteString("WEBVTT\n\n")
	for _, c := range merged {
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttTime(c.start), vttTime(c.end), c.text)
	}
	return b.Bytes()
}

var (
	reSignStyle  = regexp.MustCompile(`(?i)sign|^ts\b|title|kara|romaji|kanji|song|lyric|^op\d*\b|^ed\d*\b|opening|ending|screen|insert|note|logo`)
	reDrawing    = regexp.MustCompile(`\\p[1-9]`)
	rePositioned = regexp.MustCompile(`\\(pos|move)\(`)
	reHeavyTags  = regexp.MustCompile(`\\(fr[xyz]?|fa[xy]|i?clip|org|t)\(|\\fr[xyz]?-?\d|\\fa[xy]-?\d|\\k[fo]?\d`)
	reTag        = regexp.MustCompile(`\{[^}]*\}`)
)

// isTypesetting guesses whether an event is a sign or effect rather than
// spoken dialogue.
func isTypesetting(style, effect, text string) bool {
	if reDrawing.MatchString(text) || reSignStyle.MatchString(style) || strings.TrimSpace(effect) != "" {
		return true
	}
	return rePositioned.MatchString(text) && reHeavyTags.MatchString(text)
}

// assText strips override tags, keeping italics, and escapes for WebVTT.
func assText(s string) string {
	var b strings.Builder
	italic := false
	for len(s) > 0 {
		i := strings.IndexByte(s, '{')
		if i < 0 {
			b.WriteString(assEscape(s))
			break
		}
		b.WriteString(assEscape(s[:i]))
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			break
		}
		tag := s[i : i+j+1]
		s = s[i+j+1:]
		if k := strings.LastIndex(tag, `\i`); k >= 0 && k+2 < len(tag) && (tag[k+2] == '0' || tag[k+2] == '1') {
			on := tag[k+2] == '1'
			if on && !italic {
				b.WriteString("<i>")
			} else if !on && italic {
				b.WriteString("</i>")
			}
			italic = on
		}
	}
	if italic {
		b.WriteString("</i>")
	}
	out := strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(b.String())
	lines := strings.Split(out, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" && t != "<i></i>" {
			kept = append(kept, t)
		}
	}
	return strings.Join(kept, "\n")
}

func assEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(reTag.ReplaceAllString(s, ""))
}

func parseASSTime(s string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return float64(h)*3600 + float64(m)*60 + sec, nil
}

func vttTime(t float64) string {
	ms := int64(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
