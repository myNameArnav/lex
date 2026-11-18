package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"lex/internal/redact"
	"lex/internal/stream"
)

// report is a diagnostic report for bug reports: versions, the host's
// state and load over the last hour, settings, current playback, the media
// cache and the recent log, with personal details replaced (redact).
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	red := s.redactor()
	b := &strings.Builder{}
	line := func(format string, args ...any) { b.WriteString(red.Line(fmt.Sprintf(format, args...)) + "\n") }
	head := func(t string) { fmt.Fprintf(b, "\n== %s ==\n", t) }
	now := time.Now()

	line("Lex diagnostic report")
	line("Generated %s · Lex %s · %s/%s", now.UTC().Format(time.RFC3339), s.Version, runtime.GOOS, runtime.GOARCH)
	line("User names, titles, file names and paths, IP addresses, host names and secrets are replaced with placeholders.")

	if s.Stats != nil {
		snap := s.Stats.Snapshot()
		head("Host")
		line("%s · %s · %d cores · up %s", snap.Model, snap.OS, snap.NumCPU, (time.Duration(snap.Uptime) * time.Second).String())
		line("memory %s total, %s available · swap %s of %s used", gib(snap.MemTotal), gib(snap.MemAvail), gib(snap.SwapUsed), gib(snap.SwapTotal))
		line("now: %s", snap.Summary())
		if st := snap.Strains(); len(st) > 0 {
			for _, x := range st {
				line("overloaded: %s", x.Text)
			}
		} else {
			line("overloaded: no")
		}
		if len(snap.ThrottleFlags) > 0 {
			line("throttle flags: %s", strings.Join(snap.ThrottleFlags, ", "))
		}
		for i, d := range snap.Disks {
			line("filesystem %d: %s free of %s", i+1, gib(d.Free), gib(d.Total))
		}
		line("lex process: %s RSS, %.0f%% CPU, %d goroutines", gib(snap.ProcRSS), snap.ProcCPU, snap.Goroutines)
		head("Load over the last hour (every 30s)")
		line("time      cpu%%  iowait%%  mem%%  swap/s    disk r/s  disk w/s  net tx/s  stream/s  sessions")
		hist := s.Stats.History()
		for i, p := range hist {
			if i%10 != 0 && i != len(hist)-1 {
				continue
			}
			line("%s  %4.0f  %7.0f  %4.0f  %-8s  %-8s  %-8s  %-8s  %-8s  %d", time.Unix(p.T, 0).UTC().Format("15:04:05"),
				p.CPU, p.IOWait, p.Mem, gib(uint64(p.SwapIO)), gib(uint64(p.DiskR)), gib(uint64(p.DiskW)), gib(uint64(p.NetTx)), gib(uint64(p.Stream)), p.Sessions)
		}
	}
	if s.FF.Version != "" {
		head("ffmpeg")
		line("version %s · encoders %s · HEVC hardware decode %v%s", s.FF.Version, strings.Join(s.FF.Encoders, ", "), s.FF.HEVCHWDecode, iff(s.FF.HEVCHWDecodeIssue != "", " ("+s.FF.HEVCHWDecodeIssue+")", ""))
	}

	head("Settings")
	for _, kv := range safeConfig(s.St.Config()) {
		line("%s", kv)
	}

	head("Playback now")
	var sessions []stream.Session
	if s.Sess != nil {
		sessions = s.Sess.Snapshot()
	}
	if len(sessions) == 0 {
		line("none")
	}
	for _, x := range sessions {
		line("%s · %s · %s %s→%s %s→%s · %s %s · %s/s · file reads %.0f%% · at %.0fs, paused %v, %d restarts · reasons: %s",
			x.UserName, x.Title, x.Method, x.VideoIn, x.VideoOut, x.AudioIn, x.AudioOut, x.Client, iff(x.Remote, "remote", "local"),
			gib(uint64(x.Rate)), x.DiskWait*100, x.Position, x.Paused, x.Restarts, strings.Join(x.Reasons, "; "))
		if c := x.Client_; c.Bandwidth > 0 || c.BufferEvents > 0 {
			line("  player: %.1f Mbps, %.0fs buffered ahead, %d stalls (%.0fs), %d/%d frames dropped", c.Bandwidth/1e6, c.BufferAhead, c.BufferEvents, c.BufferSeconds, c.DroppedFrames, c.TotalFrames)
		}
		if x.Job != nil {
			j, _ := json.Marshal(x.Job)
			line("  ffmpeg: %s", j)
		}
	}

	if s.Cache != nil {
		head("Media cache")
		cs := s.Cache.Status()
		line("enabled %v · %d files, %s of %s · filesystem %s free · hits %d, misses %d", cs.Enabled, cs.Files, gib(uint64(cs.UsedBytes)), gib(uint64(cs.MaxBytes)), gib(uint64(cs.DiskFree)), cs.Hits, cs.Misses)
		if c := cs.Current; c != nil {
			line("copying %s: %s of %s at %s/s (%s)", c.Name, gib(uint64(c.Done)), gib(uint64(c.Size)), gib(uint64(c.Speed)), c.Reason)
		}
		if len(cs.Queued) > 0 {
			line("%d queued", len(cs.Queued))
		}
		if cs.LastError != "" {
			line("last error: %s", cs.LastError)
		}
	}

	head("Log")
	for _, l := range s.Log.Lines() {
		line("%s %-5s %s", time.UnixMilli(l.Time).UTC().Format("2006-01-02 15:04:05"), l.Level, l.Msg)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="lex-report-`+now.UTC().Format("20060102-150405")+`.txt"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(b.String()))
}

// redactor knows this server's users, titles, files, library folders and
// host name, to hide them in a report.
func (s *Server) redactor() *redact.Redactor {
	k := redact.Known{Paths: map[string]string{s.DataDir: "<data>"}}
	if users, err := s.St.Users(); err == nil {
		for _, u := range users {
			k.Users = append(k.Users, u.Name)
		}
	}
	if rows, err := s.St.DB().Query(`SELECT title FROM items UNION SELECT original_title FROM items WHERE original_title != ''`); err == nil {
		for rows.Next() {
			var t string
			if rows.Scan(&t) == nil {
				k.Titles = append(k.Titles, t)
			}
		}
		rows.Close()
	}
	if files, err := s.St.AllFiles(); err == nil {
		for _, f := range files {
			k.Files = append(k.Files, f.Path)
		}
	}
	if libs, err := s.St.Libraries(); err == nil {
		n := 0
		for _, l := range libs {
			for _, p := range l.Paths {
				n++
				k.Paths[p] = fmt.Sprintf("<library %d>", n)
			}
		}
	}
	if s.Cache != nil {
		k.Paths[s.Cache.Dir()] = "<cache>"
	}
	if s.Stats != nil {
		if h := s.Stats.Snapshot().Hostname; h != "" {
			k.Hosts = append(k.Hosts, h)
		}
	}
	return redact.New(k)
}

// safeConfig lists settings as "name = value", leaving out the ones that
// hold secrets, accounts, addresses or folders.
var sensitiveSetting = regexp.MustCompile(`(?i)key|token|secret|pass|user|url|dir|path|host|email|name|addr`)

func safeConfig(cfg any) []string {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []string
	for k, v := range m {
		if sensitiveSetting.MatchString(k) {
			if v != "" && v != nil {
				out = append(out, k+" = (set, hidden)")
			}
			continue
		}
		j, _ := json.Marshal(v)
		out = append(out, k+" = "+string(j))
	}
	slices.Sort(out)
	return out
}

func gib(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func iff(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
