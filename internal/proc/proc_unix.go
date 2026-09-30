//go:build unix

package proc

import (
	"os"
	"syscall"
)

func nice(pid, n int) { _ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, n) }

func suspend(p *os.Process) bool { return p.Signal(syscall.SIGSTOP) == nil }

func resume(p *os.Process) { _ = p.Signal(syscall.SIGCONT) }
