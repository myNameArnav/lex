// Package proc has small helpers for managing child processes.
package proc

import "syscall"

// Nice lowers the scheduling priority of pid. Errors are ignored: it's an
// optimisation, not a requirement.
func Nice(pid, n int) {
	if n <= 0 || pid <= 0 {
		return
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, n)
}
