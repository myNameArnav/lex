//go:build unix

package sysstats

import "syscall"

// DiskUsage returns space on the filesystem holding path; key identifies the
// filesystem so paths on the same disk are listed once.
func DiskUsage(path string) (total, free, used uint64, key [2]int64, ok bool) {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return 0, 0, 0, key, false
	}
	bs := uint64(fs.Bsize)
	total = uint64(fs.Blocks) * bs
	return total, uint64(fs.Bavail) * bs, total - uint64(fs.Bfree)*bs, [2]int64{int64(fs.Blocks), int64(fs.Bsize)}, true
}
