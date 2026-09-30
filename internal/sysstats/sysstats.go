// Package sysstats samples host metrics from /proc and /sys (Linux) into a
// ring buffer. Everything is plain file reads: no cgo, no child processes on
// the hot path.
package sysstats

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Point struct {
	T        int64   `json:"t"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"` // percent used
	Temp     float64 `json:"temp"`
	NetRx    float64 `json:"rx"` // bytes/s
	NetTx    float64 `json:"tx"`
	DiskR    float64 `json:"dr"`
	DiskW    float64 `json:"dw"`
	Stream   float64 `json:"stream"` // bytes/s delivered to players
	Sessions int     `json:"sessions"`
	ProcCPU  float64 `json:"pcpu"`
}

type Iface struct {
	Name    string  `json:"name"`
	Rx      float64 `json:"rx"`
	Tx      float64 `json:"tx"`
	RxTotal uint64  `json:"rxTotal"`
	TxTotal uint64  `json:"txTotal"`
}

type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Used  uint64 `json:"used"`
}

type Snapshot struct {
	Now           Point      `json:"now"`
	Cores         []float64  `json:"cores"`
	Load          [3]float64 `json:"load"`
	MemTotal      uint64     `json:"memTotal"`
	MemUsed       uint64     `json:"memUsed"`
	MemAvail      uint64     `json:"memAvail"`
	MemCached     uint64     `json:"memCached"`
	SwapTotal     uint64     `json:"swapTotal"`
	SwapUsed      uint64     `json:"swapUsed"`
	FreqMHz       float64    `json:"freqMhz"`
	MaxFreqMHz    float64    `json:"maxFreqMhz"`
	Uptime        float64    `json:"uptime"`
	Throttled     string     `json:"throttled"`
	ThrottleFlags []string   `json:"throttleFlags"`
	Ifaces        []Iface    `json:"ifaces"`
	Disks         []Disk     `json:"disks"`
	ProcRSS       uint64     `json:"procRss"`
	ProcCPU       float64    `json:"procCpu"`
	GoHeap        uint64     `json:"goHeap"`
	Goroutines    int        `json:"goroutines"`
	Started       int64      `json:"started"`
	Model         string     `json:"model"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	NumCPU        int        `json:"numCpu"`
	Hostname      string     `json:"hostname"`
}

type Sampler struct {
	mu       sync.Mutex
	hist     []Point
	next     int
	full     bool
	snap     Snapshot
	paths    func() []string
	streams  func() (float64, int)
	interval time.Duration

	prevCPU      [][2]uint64 // per line: idle, total
	prevNet      map[string][2]uint64
	prevDisk     [2]uint64
	prevProc     float64
	prevT        time.Time
	started      time.Time
	lastThrottle time.Time
}

func New(interval time.Duration, keep time.Duration, paths func() []string, streams func() (float64, int)) *Sampler {
	n := int(keep / interval)
	s := &Sampler{hist: make([]Point, n), paths: paths, streams: streams, interval: interval, started: time.Now(), prevNet: map[string][2]uint64{}}
	s.snap.Model = readTrim("/proc/device-tree/model")
	if s.snap.Model == "" {
		s.snap.Model = readTrim("/sys/devices/virtual/dmi/id/product_name")
	}
	s.snap.OS = osName()
	s.snap.Arch = runtime.GOARCH
	s.snap.NumCPU = runtime.NumCPU()
	s.snap.Hostname, _ = os.Hostname()
	s.snap.Started = s.started.Unix()
	s.sample()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			s.sample()
		}
	}()
	return s
}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(string(b), "\x00"))
}

func osName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func (s *Sampler) History() []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Point
	if s.full {
		out = append(out, s.hist[s.next:]...)
	}
	out = append(out, s.hist[:s.next]...)
	return out
}

func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.snap
	c.Cores = append([]float64(nil), s.snap.Cores...)
	c.Ifaces = append([]Iface(nil), s.snap.Ifaces...)
	c.Disks = append([]Disk(nil), s.snap.Disks...)
	return c
}

func (s *Sampler) sample() {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	dt := now.Sub(s.prevT).Seconds()
	first := s.prevT.IsZero()
	s.prevT = now
	p := Point{T: now.Unix()}
	snap := &s.snap

	// CPU
	if f, err := os.Open("/proc/stat"); err == nil {
		sc := bufio.NewScanner(f)
		var cores []float64
		i := 0
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "cpu") {
				break
			}
			fs := strings.Fields(line)
			var total, idle uint64
			for k, v := range fs[1:] {
				n, _ := strconv.ParseUint(v, 10, 64)
				if k < 8 { // exclude guest time (already in user)
					total += n
				}
				if k == 3 || k == 4 {
					idle += n
				}
			}
			if i >= len(s.prevCPU) {
				s.prevCPU = append(s.prevCPU, [2]uint64{})
			}
			prev := s.prevCPU[i]
			pct := 0.0
			if dtot := total - prev[1]; !first && dtot > 0 {
				pct = 100 * float64(dtot-(idle-prev[0])) / float64(dtot)
			}
			s.prevCPU[i] = [2]uint64{idle, total}
			if i == 0 {
				p.CPU = pct
			} else {
				cores = append(cores, pct)
			}
			i++
		}
		f.Close()
		snap.Cores = cores
	}
	// Load
	if b := readTrim("/proc/loadavg"); b != "" {
		fs := strings.Fields(b)
		for i := 0; i < 3 && i < len(fs); i++ {
			snap.Load[i], _ = strconv.ParseFloat(fs[i], 64)
		}
	}
	// Memory
	if f, err := os.Open("/proc/meminfo"); err == nil {
		m := map[string]uint64{}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			fs := strings.Fields(v)
			if len(fs) > 0 {
				n, _ := strconv.ParseUint(fs[0], 10, 64)
				m[k] = n * 1024
			}
		}
		f.Close()
		snap.MemTotal, snap.MemAvail = m["MemTotal"], m["MemAvailable"]
		snap.MemUsed = snap.MemTotal - snap.MemAvail
		snap.MemCached = m["Cached"] + m["Buffers"]
		snap.SwapTotal = m["SwapTotal"]
		snap.SwapUsed = m["SwapTotal"] - m["SwapFree"]
		if snap.MemTotal > 0 {
			p.Mem = 100 * float64(snap.MemUsed) / float64(snap.MemTotal)
		}
	}
	// Temperature: first thermal zone that looks like the CPU/SoC.
	if b := readTrim("/sys/class/thermal/thermal_zone0/temp"); b != "" {
		v, _ := strconv.ParseFloat(b, 64)
		p.Temp = v / 1000
	}
	if b := readTrim("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"); b != "" {
		v, _ := strconv.ParseFloat(b, 64)
		snap.FreqMHz = v / 1000
	}
	if snap.MaxFreqMHz == 0 {
		if b := readTrim("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); b != "" {
			v, _ := strconv.ParseFloat(b, 64)
			snap.MaxFreqMHz = v / 1000
		}
	}
	if b := readTrim("/proc/uptime"); b != "" {
		snap.Uptime, _ = strconv.ParseFloat(strings.Fields(b)[0], 64)
	}
	// Network (physical-ish interfaces only; tunnels would double count).
	if f, err := os.Open("/proc/net/dev"); err == nil {
		sc := bufio.NewScanner(f)
		var ifs []Iface
		for sc.Scan() {
			name, rest, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if skipIface(name) {
				continue
			}
			fs := strings.Fields(rest)
			if len(fs) < 9 {
				continue
			}
			rx, _ := strconv.ParseUint(fs[0], 10, 64)
			tx, _ := strconv.ParseUint(fs[8], 10, 64)
			it := Iface{Name: name, RxTotal: rx, TxTotal: tx}
			if prev, ok := s.prevNet[name]; ok && dt > 0 {
				it.Rx = float64(rx-prev[0]) / dt
				it.Tx = float64(tx-prev[1]) / dt
			}
			s.prevNet[name] = [2]uint64{rx, tx}
			ifs = append(ifs, it)
			if !strings.HasPrefix(name, "tailscale") && !strings.HasPrefix(name, "wg") && !strings.HasPrefix(name, "tun") {
				p.NetRx += it.Rx
				p.NetTx += it.Tx
			}
		}
		f.Close()
		snap.Ifaces = ifs
	}
	// Disk I/O across whole block devices.
	if f, err := os.Open("/proc/diskstats"); err == nil {
		sc := bufio.NewScanner(f)
		var rd, wr uint64
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 10 || !wholeDisk(fs[2]) {
				continue
			}
			r, _ := strconv.ParseUint(fs[5], 10, 64)
			w, _ := strconv.ParseUint(fs[9], 10, 64)
			rd += r * 512
			wr += w * 512
		}
		f.Close()
		if !first && dt > 0 && rd >= s.prevDisk[0] && wr >= s.prevDisk[1] {
			p.DiskR = float64(rd-s.prevDisk[0]) / dt
			p.DiskW = float64(wr-s.prevDisk[1]) / dt
		}
		s.prevDisk = [2]uint64{rd, wr}
	}
	// Our own process.
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		st := string(b)
		if i := strings.LastIndexByte(st, ')'); i > 0 {
			fs := strings.Fields(st[i+2:])
			if len(fs) > 21 {
				ut, _ := strconv.ParseFloat(fs[11], 64)
				stt, _ := strconv.ParseFloat(fs[12], 64)
				secs := (ut + stt) / 100
				if !first && dt > 0 {
					p.ProcCPU = (secs - s.prevProc) / dt * 100
				}
				s.prevProc = secs
				rss, _ := strconv.ParseUint(fs[21], 10, 64)
				snap.ProcRSS = rss * uint64(os.Getpagesize())
			}
		}
	} else {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		snap.ProcRSS = ms.Sys
	}
	snap.ProcCPU = p.ProcCPU
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	snap.GoHeap = ms.HeapAlloc
	snap.Goroutines = runtime.NumGoroutine()
	// Disk space for data + library paths, one entry per filesystem.
	if s.paths != nil {
		seen := map[[2]int64]bool{}
		var disks []Disk
		for _, pth := range s.paths() {
			var fs syscall.Statfs_t
			if syscall.Statfs(pth, &fs) != nil {
				continue
			}
			key := [2]int64{int64(fs.Blocks), int64(fs.Bsize)}
			if seen[key] {
				continue
			}
			seen[key] = true
			total := fs.Blocks * uint64(fs.Bsize)
			free := fs.Bavail * uint64(fs.Bsize)
			disks = append(disks, Disk{Path: pth, Total: total, Free: free, Used: total - fs.Bfree*uint64(fs.Bsize)})
		}
		snap.Disks = disks
	}
	// Raspberry Pi throttling flags (cheap, but a child process: every 30s).
	if time.Since(s.lastThrottle) > 30*time.Second {
		s.lastThrottle = time.Now()
		if path, err := exec.LookPath("vcgencmd"); err == nil {
			if out, err := exec.Command(path, "get_throttled").Output(); err == nil {
				v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "throttled="))
				snap.Throttled = v
				snap.ThrottleFlags = throttleFlags(v)
			}
		}
	}
	if s.streams != nil {
		p.Stream, p.Sessions = s.streams()
	}
	snap.Now = p
	s.hist[s.next] = p
	s.next = (s.next + 1) % len(s.hist)
	if s.next == 0 {
		s.full = true
	}
}

func skipIface(n string) bool {
	for _, pre := range []string{"lo", "veth", "docker", "br-", "podman", "cni", "virbr", "flannel", "vnet"} {
		if strings.HasPrefix(n, pre) {
			return true
		}
	}
	return false
}

func wholeDisk(n string) bool {
	switch {
	case strings.HasPrefix(n, "sd") || strings.HasPrefix(n, "vd") || strings.HasPrefix(n, "hd") || strings.HasPrefix(n, "xvd"):
		last := n[len(n)-1]
		return last < '0' || last > '9'
	case strings.HasPrefix(n, "mmcblk") || strings.HasPrefix(n, "nvme"):
		return !strings.Contains(n, "p") || strings.HasPrefix(n, "nvme") && !strings.Contains(n[4:], "p")
	}
	return false
}

func throttleFlags(hex string) []string {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
	if err != nil {
		return nil
	}
	names := map[int]string{
		0: "under-voltage now", 1: "frequency capped now", 2: "throttled now", 3: "soft temp limit now",
		16: "under-voltage occurred", 17: "frequency capping occurred", 18: "throttling occurred", 19: "soft temp limit occurred",
	}
	var out []string
	for bit := 0; bit < 20; bit++ {
		if v&(1<<bit) != 0 {
			if n, ok := names[bit]; ok {
				out = append(out, n)
			}
		}
	}
	return out
}

// DirSize is a helper for cache sizes.
func DirSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total
}
