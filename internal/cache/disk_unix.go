//go:build unix

package cache

import (
	"os"
	"syscall"
)

// sameDisk reports whether a and b are on the same filesystem.
func sameDisk(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	sa, ok1 := fa.Sys().(*syscall.Stat_t)
	sb, ok2 := fb.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && uint64(sa.Dev) == uint64(sb.Dev)
}
