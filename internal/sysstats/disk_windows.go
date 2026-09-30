//go:build windows

package sysstats

import "golang.org/x/sys/windows"

func DiskUsage(path string) (total, free, used uint64, key [2]int64, ok bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, key, false
	}
	var avail, tot, totFree uint64
	if windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totFree) != nil {
		return 0, 0, 0, key, false
	}
	return tot, avail, tot - totFree, [2]int64{int64(tot), int64(totFree)}, true
}
