package sysstats

import (
	"strings"
	"testing"
)

func TestStrains(t *testing.T) {
	ok := Snapshot{Now: Point{CPU: 60, IOWait: 5}, MemTotal: 2 << 30, MemAvail: 500 << 20}
	if s := ok.Strains(); s != nil {
		t.Fatalf("healthy host: %v", s)
	}
	// The case that prompted it: a USB drive shared with torrent downloads.
	disk := ok
	disk.Now.IOWait = 65
	if s := disk.Strains(); len(s) != 1 || s[0].Kind != "disk" || !strings.Contains(s[0].Text, "65%") {
		t.Fatalf("disk: %v", s)
	}
	mem := ok
	mem.MemAvail = 50 << 20
	mem.Now.SwapIO = 8e6
	if s := mem.Strains(); len(s) != 1 || s[0].Kind != "memory" || !strings.Contains(s[0].Text, "50 MB free") || !strings.Contains(s[0].Text, "swapping") {
		t.Fatalf("memory: %v", s)
	}
	cpu := ok
	cpu.Now.CPU = 99
	cpu.ThrottleFlags = []string{"under-voltage now", "throttling occurred"}
	s := cpu.Strains()
	if len(s) != 2 || s[0].Kind != "throttled" || !strings.Contains(s[0].Text, "under-voltage") || s[1].Kind != "cpu" {
		t.Fatalf("cpu + throttled: %v", s)
	}
	// Only past throttling: not a strain now.
	past := ok
	past.ThrottleFlags = []string{"throttling occurred"}
	if s := past.Strains(); s != nil {
		t.Fatalf("past throttling: %v", s)
	}
}

// High I/O wait from the library HDD (downloads, SSD cache copies) is not
// a strain for a stream read from the idle SSD, and a busy read disk is one
// even when I/O wait is low.
func TestStrainsReading(t *testing.T) {
	s := Snapshot{Now: Point{CPU: 20, IOWait: 71}, MemTotal: 2 << 30, MemAvail: 500 << 20,
		DiskBusy: map[string]float64{"sda2": 3, "sdb1": 99}}
	if got := s.StrainsReading("sda2"); got != nil {
		t.Fatalf("reading from the idle SSD: %v", got)
	}
	if got := s.StrainsReading("sdb1"); len(got) != 1 || got[0].Kind != "disk" || !strings.Contains(got[0].Text, "99% busy") {
		t.Fatalf("reading from the busy HDD: %v", got)
	}
	// Unknown device (overlay, not Linux): back to host-wide I/O wait.
	for _, dev := range []string{"", "md0"} {
		if got := s.StrainsReading(dev); len(got) != 1 || !strings.Contains(got[0].Text, "71% I/O wait") {
			t.Fatalf("unknown device %q: %v", dev, got)
		}
	}
	s.Now.IOWait = 5
	if got := s.StrainsReading("sdb1"); len(got) != 1 || got[0].Kind != "disk" {
		t.Fatalf("busy read disk, low I/O wait: %v", got)
	}
}

func TestSummary(t *testing.T) {
	s := Snapshot{Now: Point{CPU: 12, IOWait: 65}, Load: [3]float64{11.8}, MemAvail: 512 << 20, SwapUsed: 1 << 30, ThrottleFlags: []string{"under-voltage now", "throttling occurred"}}
	got := s.Summary()
	for _, want := range []string{"cpu 12%", "iowait 65%", "load 11.8", "512 MB free", "1.0 GB used", "[under-voltage now]"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "occurred") {
		t.Errorf("summary %q includes past throttling", got)
	}
}

func TestStrainWatch(t *testing.T) {
	var w StrainWatch
	ok := Snapshot{Now: Point{CPU: 10}, MemTotal: 2 << 30, MemAvail: 1 << 30}
	busy := ok
	busy.Now.IOWait = 70
	if l, _ := w.Update(ok); l != "" {
		t.Fatalf("healthy: %q", l)
	}
	if l, _ := w.Update(busy); l != "" {
		t.Fatalf("one busy sample logged: %q", l)
	}
	l, warn := w.Update(busy)
	if !warn || !strings.Contains(l, "overloaded") || !strings.Contains(l, "iowait 70%") {
		t.Fatalf("lasting strain: %q %v", l, warn)
	}
	if l, _ := w.Update(busy); l != "" {
		t.Fatalf("repeated: %q", l)
	}
	w.Update(ok)
	if l, warn := w.Update(ok); warn || !strings.Contains(l, "back to normal") {
		t.Fatalf("recovery: %q %v", l, warn)
	}
}
