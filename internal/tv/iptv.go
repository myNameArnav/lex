package tv

// IPTV: playlists (M3U) to pick live channels from, and their guides
// (XMLTV). Both are fetched by the server with the source's User-Agent.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"lex/internal/store"
)

// PlaylistEntry is a channel in an M3U playlist.
type PlaylistEntry struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Logo      string `json:"logo"`
	Group     string `json:"group"`
	GuideID   string `json:"guideId"`
	UserAgent string `json:"userAgent"`
	Referrer  string `json:"referrer"`
	// Headers: any others the playlist asks for (#EXTHTTP, or Kodi's
	// "url|Name=value").
	Headers []store.TVHeader `json:"headers"`
}

// maxDownload caps a playlist or guide download (a big XMLTV guide
// unpacks to a few hundred MB; only the channels in the lineup are kept).
const maxDownload = 512 << 20

// ParseM3U reads an extended M3U playlist: #EXTINF lines (tvg-id,
// tvg-logo, group-title, then the name after the comma), #EXTVLCOPT
// http-user-agent / http-referrer, #EXTHTTP {"Header": "value"}, #EXTGRP,
// and Kodi's "url|User-Agent=…&Other=…" suffix (values URL-encoded).
// Entries that aren't http(s) are left out.
func ParseM3U(r io.Reader) ([]PlaylistEntry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var out []PlaylistEntry
	var cur PlaylistEntry
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXTINF:"):
			cur = PlaylistEntry{}
			info := line[len("#EXTINF:"):]
			// The name follows the first comma outside quotes.
			inQ, cut := false, -1
			for i, c := range info {
				if c == '"' {
					inQ = !inQ
				} else if c == ',' && !inQ {
					cut = i
					break
				}
			}
			attrs := info
			if cut >= 0 {
				attrs, cur.Name = info[:cut], strings.TrimSpace(info[cut+1:])
			}
			a := m3uAttrs(attrs)
			cur.GuideID, cur.Logo, cur.Group = a["tvg-id"], a["tvg-logo"], a["group-title"]
			if cur.Name == "" {
				cur.Name = a["tvg-name"]
			}
			if ua := a["http-user-agent"]; ua != "" {
				cur.UserAgent = ua
			}
		case strings.HasPrefix(line, "#EXTVLCOPT:"):
			k, v, _ := strings.Cut(line[len("#EXTVLCOPT:"):], "=")
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "http-user-agent":
				cur.UserAgent = strings.TrimSpace(v)
			case "http-referrer", "http-referer":
				cur.Referrer = strings.TrimSpace(v)
			}
		case strings.HasPrefix(line, "#EXTHTTP:"):
			var hs map[string]string
			if json.Unmarshal([]byte(line[len("#EXTHTTP:"):]), &hs) == nil {
				keys := make([]string, 0, len(hs))
				for k := range hs {
					keys = append(keys, k)
				}
				slices.Sort(keys)
				for _, k := range keys {
					cur.Headers = append(cur.Headers, store.TVHeader{Name: k, Value: hs[k]})
				}
			}
		case strings.HasPrefix(line, "#EXTGRP:"):
			if cur.Group == "" {
				cur.Group = strings.TrimSpace(line[len("#EXTGRP:"):])
			}
		case strings.HasPrefix(line, "#"):
		default:
			u, opts, _ := strings.Cut(line, "|")
			cur.URL = strings.TrimSpace(u)
			for _, kv := range strings.Split(opts, "&") {
				k, v, _ := strings.Cut(kv, "=")
				if dv, err := url.QueryUnescape(v); err == nil {
					v = dv
				}
				if k = strings.TrimSpace(k); k != "" {
					cur.Headers = append(cur.Headers, store.TVHeader{Name: k, Value: v})
				}
			}
			// A User-Agent or Referer among them goes to its own field.
			var ua, ref string
			cur.Headers, ua, ref = store.CleanHeaders(cur.Headers)
			if ua != "" {
				cur.UserAgent = ua
			}
			if ref != "" {
				cur.Referrer = ref
			}
			if store.WebURL(cur.URL) {
				if cur.Name == "" {
					cur.Name = cur.URL
				}
				out = append(out, cur)
			}
			cur = PlaylistEntry{}
		}
	}
	return out, sc.Err()
}

// m3uAttrs reads key="value" pairs.
func m3uAttrs(s string) map[string]string {
	out := map[string]string{}
	for {
		eq := strings.Index(s, `="`)
		if eq < 0 {
			return out
		}
		key := s[:eq]
		if sp := strings.LastIndexAny(key, " \t"); sp >= 0 {
			key = key[sp+1:]
		}
		rest := s[eq+2:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return out
		}
		out[strings.ToLower(key)] = strings.TrimSpace(rest[:end])
		s = rest[end+1:]
	}
}

// fetch downloads u with ua and headers (a gzip file or response is
// unpacked).
func fetch(ctx context.Context, u, ua string, headers []store.TVHeader) (io.ReadCloser, error) {
	if !store.WebURL(u) {
		return nil, errors.New("not an http(s) address")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if ua == "" {
		ua = "lex"
	}
	for _, hd := range headers {
		req.Header.Set(hd.Name, hd.Value)
	}
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	br := bufio.NewReader(io.LimitReader(resp.Body, maxDownload))
	if magic, _ := br.Peek(2); bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(br)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(gz, maxDownload), resp.Body}, nil
	}
	return struct {
		io.Reader
		io.Closer
	}{br, resp.Body}, nil
}

type playlist struct {
	entries []PlaylistEntry
	at      time.Time
	err     error
}

// Playlist reads a source's channels; kept for an hour (a failed read for
// a minute). Its User-Agent is the default for its channels.
func (g *Guide) Playlist(ctx context.Context, src store.TVSource) ([]PlaylistEntry, error) {
	key := fmt.Sprint(src.URL, src.UserAgent, src.Headers)
	g.mu.Lock()
	p, ok := g.playlists[key]
	g.mu.Unlock()
	if ok && (time.Since(p.at) < time.Hour && p.err == nil || time.Since(p.at) < time.Minute) {
		return p.entries, p.err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var entries []PlaylistEntry
	body, err := fetch(ctx, src.URL, src.UserAgent, src.Headers)
	if err == nil {
		entries, err = ParseM3U(body)
		body.Close()
	}
	for i := range entries {
		if entries[i].UserAgent == "" {
			entries[i].UserAgent = src.UserAgent
		}
		if len(entries[i].Headers) == 0 {
			entries[i].Headers = src.Headers
		}
		if entries[i].Headers == nil {
			entries[i].Headers = []store.TVHeader{}
		}
	}
	g.mu.Lock()
	g.playlists[key] = playlist{entries, time.Now(), err}
	g.mu.Unlock()
	return entries, err
}

// GuideProgram is a program in an XMLTV guide.
type GuideProgram struct {
	Title       string
	Subtitle    string
	Description string
	Start, End  time.Time
}

// epg holds the guide programs of the streams in the lineup, by guide id.
type epg struct {
	mu      sync.Mutex
	progs   map[string][]GuideProgram
	at      time.Time // last refresh started
	wanted  string    // the sources and guide ids it was made for
	loading bool
	err     error
}

// guideFor returns a stream's guide programs in [from, to). The guides are
// read in the background, every six hours or when the streams change, so
// this never waits on them.
func (g *Guide) guideFor(st *store.TVStream, from, to time.Time) []GuideProgram {
	cfg := g.St.Config()
	g.refreshGuides(cfg)
	if st.GuideID == "" {
		return nil
	}
	g.epg.mu.Lock()
	defer g.epg.mu.Unlock()
	var out []GuideProgram
	for _, p := range g.epg.progs[st.GuideID] {
		if p.End.After(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	return out
}

// GuideStatus reports when the guides were last read and why that failed.
func (g *Guide) GuideStatus() (time.Time, error) {
	g.epg.mu.Lock()
	defer g.epg.mu.Unlock()
	return g.epg.at, g.epg.err
}

func (g *Guide) refreshGuides(cfg store.Config) {
	ids := map[string]bool{}
	for _, st := range cfg.TVStreams {
		if st.GuideID != "" {
			ids[st.GuideID] = true
		}
	}
	var srcs []store.TVSource
	for _, src := range cfg.TVSources {
		if src.GuideURL != "" {
			srcs = append(srcs, src)
		}
	}
	keys := make([]string, 0, len(ids))
	for k := range ids {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wanted := fmt.Sprint(srcs, keys)
	e := &g.epg
	e.mu.Lock()
	if e.loading || (e.wanted == wanted && time.Since(e.at) < 6*time.Hour) {
		e.mu.Unlock()
		return
	}
	if len(srcs) == 0 || len(ids) == 0 {
		e.progs, e.wanted, e.at, e.err = nil, wanted, time.Now(), nil
		e.mu.Unlock()
		return
	}
	e.loading, e.at = true, time.Now()
	e.mu.Unlock()
	go func() {
		progs := map[string][]GuideProgram{}
		var errs []error
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, src := range srcs {
			if err := readGuide(ctx, src, ids, progs); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", src.Name, err))
			}
		}
		for k := range progs {
			slices.SortFunc(progs[k], func(a, b GuideProgram) int { return a.Start.Compare(b.Start) })
		}
		e.mu.Lock()
		e.progs, e.wanted, e.loading, e.err = progs, wanted, false, errors.Join(errs...)
		e.mu.Unlock()
	}()
}

// readGuide adds src's programs for the channels in ids, from a day back
// to two days on.
func readGuide(ctx context.Context, src store.TVSource, ids map[string]bool, into map[string][]GuideProgram) error {
	body, err := fetch(ctx, src.GuideURL, src.UserAgent, src.Headers)
	if err != nil {
		return err
	}
	defer body.Close()
	return ParseXMLTV(body, ids, time.Now().Add(-24*time.Hour), time.Now().Add(48*time.Hour), into)
}

// ParseXMLTV adds the <programme>s of the channels in ids that overlap
// [from, to) to into.
func ParseXMLTV(r io.Reader, ids map[string]bool, from, to time.Time, into map[string][]GuideProgram) error {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "programme" {
			continue
		}
		var ch, start, stop string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "channel":
				ch = a.Value
			case "start":
				start = a.Value
			case "stop":
				stop = a.Value
			}
		}
		if !ids[ch] {
			if err := dec.Skip(); err != nil {
				return err
			}
			continue
		}
		var p struct {
			Title []string `xml:"title"`
			Sub   []string `xml:"sub-title"`
			Desc  []string `xml:"desc"`
		}
		if err := dec.DecodeElement(&p, &se); err != nil {
			return err
		}
		s, err1 := xmltvTime(start)
		e, err2 := xmltvTime(stop)
		if err1 != nil || err2 != nil || !e.After(s) || !e.After(from) || !s.Before(to) {
			continue
		}
		gp := GuideProgram{Start: s, End: e}
		if len(p.Title) > 0 {
			gp.Title = strings.TrimSpace(p.Title[0])
		}
		if len(p.Sub) > 0 {
			gp.Subtitle = strings.TrimSpace(p.Sub[0])
		}
		if len(p.Desc) > 0 {
			gp.Description = strings.TrimSpace(p.Desc[0])
		}
		into[ch] = append(into[ch], gp)
	}
}

// xmltvTime reads "20261119203000 +0100" (the zone may be left out: UTC).
func xmltvTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse("20060102150405 -0700", v); err == nil {
		return t, nil
	}
	if len(v) >= 14 {
		return time.Parse("20060102150405", v[:14])
	}
	return time.Time{}, fmt.Errorf("bad time %q", v)
}
