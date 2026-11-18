package api

import "testing"

func TestRangeStart(t *testing.T) {
	for _, c := range []struct {
		h      string
		off, n int64
		ok     bool
	}{
		{"bytes=100-", 100, -1, true},
		{"bytes=0-1023", 0, 1024, true},
		{"bytes=-500", 0, 0, false},
		{"bytes=0-1,5-9", 0, 0, false},
		{"", 0, 0, false},
		{"bytes=9-3", 0, 0, false},
	} {
		off, n, ok := rangeStart(c.h)
		if off != c.off || n != c.n || ok != c.ok {
			t.Errorf("%q: %d %d %v", c.h, off, n, ok)
		}
	}
}
