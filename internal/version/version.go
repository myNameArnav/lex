// Package version reports which build of Lex is running.
package version

import (
	_ "embed"
	"runtime/debug"
	"strings"
)

// Release is the version number, kept in the VERSION file next to this one.
// Release tags must match it (v + Release).
//
//go:embed VERSION
var release string

// Commit can be set at build time when the Git metadata isn't available to
// the Go toolchain (container builds):
//
//	-ldflags "-X lex/internal/version.Commit=$(git rev-parse --short HEAD)"
var Commit string

// Release returns the plain version number, e.g. "0.0.2".
func Release() string { return strings.TrimSpace(release) }

// String returns the version with the commit it was built from, when known:
// "0.0.2 (0ac9413)", or "0.0.2 (0ac9413, modified)" for uncommitted changes.
func String() string {
	v := Release()
	if c := commit(); c != "" {
		v += " (" + c + ")"
	}
	return v
}

func commit() string {
	if Commit != "" {
		return shorten(Commit)
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return ""
	}
	if dirty {
		return shorten(rev) + ", modified"
	}
	return shorten(rev)
}

func shorten(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
