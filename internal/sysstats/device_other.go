//go:build !linux

package sysstats

// Device returns "" off Linux: there is no per-device busy time to match.
func Device(path string) string { return "" }
