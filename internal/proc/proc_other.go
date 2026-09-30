//go:build !unix

package proc

import "os"

// Windows has no nice levels or stop signals: children run at normal
// priority and HLS encoders are never paused (they finish instead).

func nice(pid, n int) {}

func suspend(p *os.Process) bool { return false }

func resume(p *os.Process) {}
