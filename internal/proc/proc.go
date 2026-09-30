// Package proc has small helpers for managing child processes.
package proc

import "os"

// Nice lowers the scheduling priority of pid. Errors are ignored: it's an
// optimisation, not a requirement.
func Nice(pid, n int) {
	if n <= 0 || pid <= 0 {
		return
	}
	nice(pid, n)
}

// Suspend pauses a process (throttling a producer that is far ahead). It
// reports whether the process was paused; not every platform supports it.
func Suspend(p *os.Process) bool { return p != nil && suspend(p) }

// Resume continues a process paused by Suspend.
func Resume(p *os.Process) {
	if p != nil {
		resume(p)
	}
}
