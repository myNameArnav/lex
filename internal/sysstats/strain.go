package sysstats

import (
	"fmt"
	"slices"
	"strings"
)

// Strain is something overloading the host right now, which can make
// playback stall whatever the viewer's connection: a busy or slow disk,
// a maxed-out CPU, too little memory, or a throttled Raspberry Pi.
type Strain struct {
	Kind string `json:"kind"` // disk | cpu | memory | throttled
	// Text says what's wrong, with the numbers ("Server CPU at 97%.").
	Text string `json:"text"`
}

// Limits for Strains, chosen so a healthy but busy Pi doesn't trip them:
// a transcode alone keeps the CPU near 100%, so CPU counts only above 95%.
const (
	strainIOWait   = 30   // % of CPU time waiting on disk
	strainCPU      = 95   // % busy
	strainMemAvail = 0.05 // share of RAM available
	strainSwapIO   = 4e6  // bytes/s swapped
)

// Strains names what is overloading the host in this snapshot, worst
// first, or nil when it is fine.
func (s Snapshot) Strains() []Strain {
	var out []Strain
	p := s.Now
	if slices.ContainsFunc(s.ThrottleFlags, func(f string) bool {
		return f == "under-voltage now" || f == "throttled now" || f == "frequency capped now"
	}) {
		why := "overheating"
		if p.Temp > 0 {
			why = fmt.Sprintf("overheating (%.0f°C)", p.Temp)
		}
		if slices.Contains(s.ThrottleFlags, "under-voltage now") {
			why = "under-voltage"
		}
		out = append(out, Strain{"throttled", "Server CPU throttled: " + why + "."})
	}
	if p.IOWait >= strainIOWait {
		out = append(out, Strain{"disk", fmt.Sprintf("Server disk overloaded (%.0f%% I/O wait).", p.IOWait)})
	}
	if s.MemTotal > 0 && (float64(s.MemAvail) < strainMemAvail*float64(s.MemTotal) || p.SwapIO >= strainSwapIO) {
		t := fmt.Sprintf("Server low on memory (%s free", mb(s.MemAvail))
		if p.SwapIO >= strainSwapIO {
			t += fmt.Sprintf(", swapping %s/s", mb(uint64(p.SwapIO)))
		}
		out = append(out, Strain{"memory", t + ")."})
	}
	if p.CPU >= strainCPU {
		out = append(out, Strain{"cpu", fmt.Sprintf("Server CPU at %.0f%%.", p.CPU)})
	}
	return out
}

// Summary is a one-line account of the host's load for logs, e.g.
// "cpu 12% iowait 65% load 11.8 mem 512 MB free swap 1.0 GB used, 0 B/s".
func (s Snapshot) Summary() string {
	p := s.Now
	b := &strings.Builder{}
	fmt.Fprintf(b, "cpu %.0f%% iowait %.0f%% load %.1f mem %s free swap %s used, %s/s disk r %s/s w %s/s",
		p.CPU, p.IOWait, s.Load[0], mb(s.MemAvail), mb(s.SwapUsed), mb(uint64(p.SwapIO)), mb(uint64(p.DiskR)), mb(uint64(p.DiskW)))
	if p.Temp > 0 {
		fmt.Fprintf(b, " temp %.0f°C", p.Temp)
	}
	for _, f := range s.ThrottleFlags {
		if strings.HasSuffix(f, " now") {
			fmt.Fprintf(b, " [%s]", f)
		}
	}
	return b.String()
}

func mb(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// StrainWatch turns snapshots into log lines when the host becomes
// overloaded and when it recovers. A strain must last two samples in a
// row before it is reported, so a single busy moment stays quiet.
type StrainWatch struct {
	reported string // kinds last logged, e.g. "disk,memory"
	pending  string
}

// Update returns a line to log (warn: the host is strained), or "".
func (w *StrainWatch) Update(s Snapshot) (line string, warn bool) {
	strains := s.Strains()
	kinds := make([]string, len(strains))
	texts := make([]string, len(strains))
	for i, st := range strains {
		kinds[i], texts[i] = st.Kind, st.Text
	}
	key := strings.Join(kinds, ",")
	if key == w.reported {
		w.pending = key
		return "", false
	}
	if key != w.pending {
		w.pending = key
		return "", false
	}
	w.reported = key
	if key == "" {
		return "host: load back to normal (" + s.Summary() + ")", false
	}
	return "host: overloaded — " + strings.Join(texts, " ") + " (" + s.Summary() + ")", true
}
