// Package redact replaces personal details in log text with stable
// placeholders, for diagnostic reports people can share: user names,
// titles, file names and paths, IP addresses, host names, e-mail addresses
// and secrets in URLs. The same value always gets the same placeholder, so
// a report still shows that two lines are about the same user or file.
package redact

import (
	"cmp"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// Known lists the values to hide that only the server knows about.
type Known struct {
	Users []string // user names
	// Titles are names of titles (movies, shows, episodes); Files are media
	// file paths; their base names, with and without extension, are hidden too.
	Titles []string
	Files  []string
	// Paths are directories (library roots, the data directory), shown as
	// their label: {"/mnt/media/tv": "<library 2>"}.
	Paths map[string]string
	Hosts []string // host names (the server's own)
}

type Redactor struct {
	words *strings.Replacer // titles, files, paths, hosts
	users *regexp.Regexp
	user  map[string]string
	ips   map[string]string
	n     map[string]int // per kind, for numbering
}

func New(k Known) *Redactor {
	r := &Redactor{user: map[string]string{}, ips: map[string]string{}, n: map[string]int{}}
	type pair struct{ from, to string }
	var pairs []pair
	seen := map[string]bool{}
	add := func(from, to string) {
		if len(from) < 3 || seen[from] {
			return
		}
		seen[from] = true
		pairs = append(pairs, pair{from, to})
	}
	for p, label := range k.Paths {
		add(strings.TrimRight(p, "/"), label)
	}
	for i, f := range k.Files {
		label := fmt.Sprintf("<file %d>", i+1)
		ext := ""
		if j := strings.LastIndexByte(f, '.'); j > strings.LastIndexByte(f, '/') {
			ext = f[j:]
		}
		add(f, label+ext)
		base := f[strings.LastIndexByte(f, '/')+1:]
		add(base, label+ext)
		add(strings.TrimSuffix(base, ext), label)
	}
	for i, t := range k.Titles {
		add(t, fmt.Sprintf("<title %d>", i+1))
	}
	for i, h := range k.Hosts {
		add(h, fmt.Sprintf("<host %d>", i+1))
	}
	// Longest first, so a file's full path wins over its base name and a
	// title wins over a shorter title inside it.
	slices.SortStableFunc(pairs, func(a, b pair) int { return cmp.Compare(len(b.from), len(a.from)) })
	args := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		args = append(args, p.from, p.to)
	}
	r.words = strings.NewReplacer(args...)
	var names []string
	for _, u := range k.Users {
		if u == "" || r.user[u] != "" {
			continue
		}
		r.user[u] = fmt.Sprintf("<user %d>", len(r.user)+1)
		names = append(names, regexp.QuoteMeta(u))
	}
	if len(names) > 0 {
		slices.SortFunc(names, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
		r.users = regexp.MustCompile(`(^|[^\pL\pN_.@-])(` + strings.Join(names, "|") + `)($|[^\pL\pN_@-])`)
	}
	return r
}

var (
	ipv4     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6     = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{0,4}\b|(?i)\b(?:[0-9a-f]{1,4}:){1,6}:[0-9a-f]{0,4}\b`)
	email    = regexp.MustCompile(`[\w.+-]+@[\w-]+(?:\.[\w-]+)+`)
	secret   = regexp.MustCompile(`(?i)\b((?:api_?key|apikey|token|key|password|pass|secret|sid|auth)=)[^&\s"']+`)
	tailnet  = regexp.MustCompile(`(?i)\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.ts\.net\b`)
	homeDir  = regexp.MustCompile(`/(home|Users)/[^/\s"']+`)
	quoteArg = regexp.MustCompile(`(failed login for )"[^"]*"`)
)

// Line returns s with personal details replaced.
func (r *Redactor) Line(s string) string {
	s = r.words.Replace(s)
	s = secret.ReplaceAllString(s, "${1}<hidden>")
	s = email.ReplaceAllString(s, "<email>")
	s = tailnet.ReplaceAllString(s, "<tailnet host>")
	s = homeDir.ReplaceAllString(s, "/$1/<user>")
	s = quoteArg.ReplaceAllString(s, `${1}"<name>"`)
	if r.users != nil {
		// Twice: a match consumes the character after a name, so two names
		// separated by one character need a second pass.
		for range 2 {
			s = r.users.ReplaceAllStringFunc(s, func(m string) string {
				sub := r.users.FindStringSubmatch(m)
				return sub[1] + r.user[sub[2]] + sub[3]
			})
		}
	}
	s = ipv4.ReplaceAllStringFunc(s, func(m string) string { return r.ip(m) })
	s = ipv6.ReplaceAllStringFunc(s, func(m string) string {
		if _, err := netip.ParseAddr(m); err != nil {
			return m // e.g. a time like 12:34:56
		}
		return r.ip(m)
	})
	return s
}

// ip names an address by its kind (LAN, tailnet, loopback, public), which
// matters for diagnosing, and a number.
func (r *Redactor) ip(s string) string {
	if p, ok := r.ips[s]; ok {
		return p
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return s
	}
	kind := "ip"
	switch {
	case a.IsLoopback():
		return s // 127.0.0.1 and ::1 are not personal
	case a.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(a), a.Is6() && netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(a):
		kind = "tailnet ip"
	case a.IsPrivate(), a.IsLinkLocalUnicast():
		kind = "lan ip"
	}
	r.n[kind]++
	p := fmt.Sprintf("<%s %d>", kind, r.n[kind])
	r.ips[s] = p
	return p
}
