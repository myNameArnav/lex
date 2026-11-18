//go:build !unix

package cache

func sameDisk(a, b string) bool { return false }
