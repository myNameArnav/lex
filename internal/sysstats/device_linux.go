package sysstats

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Device returns the kernel name of the block device ("sdb1") holding
// path, as named in DiskBusy, or "" when unknown (e.g. an overlay or
// network filesystem).
func Device(path string) string {
	var st unix.Stat_t
	if unix.Stat(path, &st) != nil {
		return ""
	}
	dev := uint64(st.Dev) //nolint:unconvert // uint32 on some arches
	link, err := os.Readlink(fmt.Sprintf("/sys/dev/block/%d:%d", unix.Major(dev), unix.Minor(dev)))
	if err != nil {
		return ""
	}
	return filepath.Base(link)
}
