package tv

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lex/internal/store"
)

func TestParseM3U(t *testing.T) {
	in := "\ufeff#EXTM3U url-tvg=\"https://guide.test/epg.xml\"\n" +
		`#EXTINF:-1 tvg-id="One.us@SD" tvg-logo="https://img.test/1.png" group-title="Movies;News",One, the first` + "\n" +
		"https://a.test/one.m3u8\n" +
		"#EXTINF:-1 tvg-id=\"Two.uk\",Two\n" +
		"#EXTVLCOPT:http-user-agent=Player/1.0\n" +
		"#EXTVLCOPT:http-referrer=https://site.test/\n" +
		"https://b.test/two.m3u8\n" +
		"#EXTINF:-1,Three\n" +
		"https://c.test/three.ts|User-Agent=Kodi/20&Referer=https://k.test/\n" +
		"#EXTINF:-1,Local\n" +
		"file:///etc/passwd\n"
	got, err := ParseM3U(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %+v", got)
	}
	if e := got[0]; e.Name != "One, the first" || e.GuideID != "One.us@SD" || e.Logo != "https://img.test/1.png" || e.Group != "Movies;News" || e.URL != "https://a.test/one.m3u8" {
		t.Fatalf("first = %+v", e)
	}
	if e := got[1]; e.UserAgent != "Player/1.0" || e.Referrer != "https://site.test/" || e.GuideID != "Two.uk" {
		t.Fatalf("second = %+v", e)
	}
	if e := got[2]; e.URL != "https://c.test/three.ts" || e.UserAgent != "Kodi/20" || e.Referrer != "https://k.test/" || e.UserAgent == got[1].UserAgent {
		t.Fatalf("third = %+v", e)
	}
}

func TestParseXMLTV(t *testing.T) {
	now := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)
	in := `<?xml version="1.0" encoding="UTF-8"?><tv>
<channel id="One.us"><display-name>One</display-name></channel>
<programme start="20261119193000 +0000" stop="20261119210000 +0000" channel="One.us"><title lang="en">News &amp; More</title><sub-title>Evening</sub-title><desc>Today.</desc></programme>
<programme start="20261119210000 +0100" stop="20261119220000 +0100" channel="One.us"><title>Film</title></programme>
<programme start="20261110100000 +0000" stop="20261110110000 +0000" channel="One.us"><title>Old</title></programme>
<programme start="20261119193000 +0000" stop="20261119210000 +0000" channel="Other"><title>Elsewhere</title></programme>
</tv>`
	into := map[string][]GuideProgram{}
	if err := ParseXMLTV(strings.NewReader(in), map[string]bool{"One.us": true}, now.Add(-time.Hour), now.Add(48*time.Hour), into); err != nil {
		t.Fatal(err)
	}
	ps := into["One.us"]
	if len(ps) != 2 || len(into) != 1 {
		t.Fatalf("programs = %+v", into)
	}
	if p := ps[0]; p.Title != "News & More" || p.Subtitle != "Evening" || p.Description != "Today." || !p.Start.Equal(now.Add(-30*time.Minute)) {
		t.Fatalf("first = %+v", p)
	}
	if p := ps[1]; !p.Start.Equal(now) || !p.End.Equal(now.Add(time.Hour)) {
		t.Fatalf("zone not applied: %+v", p)
	}
}

// A live channel joins the lineup, but never airs library titles.
func TestStreamChannels(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB().Exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'Movies','movies','[]',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES(1,1,'movie',0,'Heat','heat',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO files(id,item_id,library_id,path,size,mtime,added_at,duration) VALUES(1,1,1,'/m/heat.mkv',1,0,0,6000)`); err != nil {
		t.Fatal(err)
	}
	c := st.Config()
	c.TVStreams = []store.TVStream{{ID: "n", Name: "News", URL: "https://a.test/n.m3u8"}, {ID: "bad", Name: "Local", URL: "file:///etc/passwd"}}
	if _, err := st.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	g := New(st)
	chs, err := g.Channels()
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 2 || chs[0].ID != "stream-n" || chs[0].Stream == nil || chs[1].ID != "lib-1" {
		t.Fatalf("channels = %+v", chs)
	}
	if progs, _ := g.Programs(chs[0], time.Now(), time.Now().Add(time.Hour)); len(progs) != 0 {
		t.Fatalf("a live channel airs %+v", progs)
	}
	if n, _ := g.Titles(chs[0]); n != 0 {
		t.Fatalf("titles = %d", n)
	}
	if libs, _ := g.Libraries(chs[0]); len(libs) != 0 {
		t.Fatalf("libraries = %v", libs)
	}
}
