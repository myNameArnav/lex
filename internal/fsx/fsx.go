// Package fsx has the helpers that clean up files Lex derives from media
// (subtitle conversions, seek previews, thumbnails). Everything here only
// ever touches a direct child of a folder the caller owns inside the data
// folder, never follows symlinks, and never touches media files.
package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Version is the "<fileID>-<mtime>" prefix that derived files and folders
// are named with, so they can be matched to the file they came from.
type Version struct {
	ID, Mtime int64
}

// ParseVersion reads the leading "<fileID>-<mtime>" of a derived file's
// name, ignoring one leading dot (temporary files). It accepts the bare
// prefix or the prefix followed by "-" or ".".
func ParseVersion(name string) (Version, bool) {
	name = strings.TrimPrefix(name, ".")
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return Version{}, false
	}
	id, err := strconv.ParseInt(name[:i], 10, 64)
	if err != nil || id <= 0 {
		return Version{}, false
	}
	rest := name[i+1:]
	j := 0
	if strings.HasPrefix(rest, "-") { // negative mtime
		j = 1
	}
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 || rest[:j] == "-" || (j < len(rest) && rest[j] != '-' && rest[j] != '.') {
		return Version{}, false
	}
	mtime, err := strconv.ParseInt(rest[:j], 10, 64)
	if err != nil {
		return Version{}, false
	}
	return Version{id, mtime}, true
}

// Entry is a direct child of a cache folder considered for removal.
type Entry struct {
	Name    string
	Dir     bool
	ModTime time.Time
}

// List returns the regular files and folders directly inside dir. Symlinks
// and anything else are left out, so they're never removed.
func List(dir string) []Entry {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]Entry, 0, len(ents))
	for _, e := range ents {
		if !e.Type().IsRegular() && !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Name: e.Name(), Dir: e.IsDir(), ModTime: info.ModTime()})
	}
	return out
}

var errUnsafe = errors.New("refusing to remove: not a plain child of the cache folder")

// Remove deletes dir/name, a file or a whole folder. name must be a single
// path element, dir must be absolute, and the entry must not be a symlink
// (RemoveAll never follows links inside a folder it deletes).
func Remove(dir, name string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) == string(filepath.Separator) ||
		name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return errUnsafe
	}
	p := filepath.Join(dir, name)
	st, err := os.Lstat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return errUnsafe
	}
	if st.IsDir() {
		return os.RemoveAll(p)
	}
	return os.Remove(p)
}
