package api

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"lex/internal/store"
	"lex/internal/stream"
)

// The fixture's files are all MP4 with H.264 and AAC.
const mp4Caps = `"caps":{"mse":true,"direct":{"mp4":true},"video":{"h264":true},"nativeVideo":{"h264":true},"audio":{"aac":true},"nativeAudio":{"aac":true}}`
const noMP4Caps = `"caps":{"mse":true,"direct":{},"video":{"h264":true},"nativeVideo":{"h264":true},"audio":{"aac":true},"nativeAudio":{"aac":true}}`

func TestCompat(t *testing.T) {
	f := newAccessFixture(t)
	post := func(token, path, body string, out any) {
		t.Helper()
		code, resp := f.do(t, token, "POST", path, body)
		if code != 200 {
			t.Fatalf("POST %s: %d %s", path, code, resp)
		}
		if err := json.Unmarshal([]byte(resp), out); err != nil {
			t.Fatal(err)
		}
	}

	var sum compatSummary
	post(f.admin, "/api/admin/compat", `{`+mp4Caps+`}`, &sum)
	if sum.Files != 6 || sum.Tiers["direct"] != 6 || len(sum.Causes) != 0 {
		t.Errorf("mp4 browser: %+v", sum)
	}
	sum = compatSummary{}
	post(f.admin, "/api/admin/compat", `{`+noMP4Caps+`}`, &sum)
	container := compatCause{Tier: "remux", Cause: stream.Cause{Stage: "direct", Kind: "container", Key: "mp4"}, Files: 6, Bytes: 108}
	if sum.Files != 6 || sum.Tiers["remux"] != 6 || !slices.Contains(sum.Causes, container) {
		t.Errorf("no mp4: %+v", sum)
	}
	// This device's quality setting is taken into account.
	sum = compatSummary{}
	post(f.admin, "/api/admin/compat", `{`+mp4Caps+`,"maxBitrate":500}`, &sum)
	if sum.Tiers["transcode"] != 6 {
		t.Errorf("500 kbps: %+v", sum)
	}

	var titles []store.LibraryTitle
	post(f.admin, "/api/admin/compat/titles", `{`+noMP4Caps+`,"tier":"remux","cause":{"stage":"direct","kind":"container","key":"mp4"}}`, &titles)
	if len(titles) != 4 {
		t.Errorf("titles: %+v", titles)
	}

	// The library-wide report is the admin's; titles are anyone's, within
	// their libraries.
	for _, p := range []string{"/api/admin/compat", "/api/admin/compat/titles"} {
		if code, _ := f.do(t, f.all, "POST", p, `{`+mp4Caps+`,"tier":"direct"}`); code != 403 {
			t.Errorf("non-admin %s: %d, want 403", p, code)
		}
	}

	var item struct {
		Files []struct {
			FileID int64  `json:"fileId"`
			Tier   string `json:"tier"`
		} `json:"files"`
	}
	post(f.kid, "/api/items/"+strconv.Itoa(familyMovie)+"/compat", `{`+mp4Caps+`}`, &item)
	if len(item.Files) != 1 || item.Files[0].FileID != f.familyMovieFile || item.Files[0].Tier != "direct" {
		t.Errorf("item compat: %+v", item)
	}
	if code, _ := f.do(t, f.kid, "POST", "/api/items/"+strconv.Itoa(privateMovie)+"/compat", `{`+mp4Caps+`}`); code != 404 {
		t.Errorf("kid item compat on a private movie: %d, want 404", code)
	}
}
