package redact

import (
	"strings"
	"testing"
)

func TestLine(t *testing.T) {
	r := New(Known{
		Users:  []string{"alexandra", "guest"},
		Titles: []string{"Night Harbour", "Lighthouse", "Megamind"},
		Files:  []string{"/mnt/media/tv/Night Harbour/Season 01/Night Harbour - S01E05 - LIGHTHOUSE HDTV-1080p.mkv"},
		Paths:  map[string]string{"/mnt/media/tv": "<library 1>", "/srv/lexdata": "<data>"},
		Hosts:  []string{"mediabox"},
	})
	cases := []struct{ in, want string }{
		{`play: alexandra — Night Harbour S01E05 · Lighthouse via remux at 3:50 (Firefox on macOS, 100.101.102.103)`,
			`play: <user 1> — <title 1> S01E05 · <title 2> via remux at 3:50 (Firefox on macOS, <tailnet ip 1>)`},
		{`cache: copying Night Harbour - S01E05 - LIGHTHOUSE HDTV-1080p.mkv (2611 MB, playing)`,
			`cache: copying <file 1>.mkv (2611 MB, playing)`},
		{`open /mnt/media/tv/Night Harbour/Season 01/Night Harbour - S01E05 - LIGHTHOUSE HDTV-1080p.mkv: no such file`,
			`open <file 1>.mkv: no such file`},
		{`scan /mnt/media/tv/Other Show: done`, `scan <library 1>/Other Show: done`},
		{`failed login for "someone" from 192.168.0.12`, `failed login for "<name>" from <lan ip 1>`},
		{`login: guest from 100.101.102.103`, `login: <user 2> from <tailnet ip 1>`},
		{`webhook http://host.containers.internal:8420/api/webhook/scan?token=abc123&x=1`, `webhook http://host.containers.internal:8420/api/webhook/scan?token=<hidden>&x=1`},
		{"served at https://box.tail0000" + ".ts.net and /ho" + "me/alexandra/lex", `served at https://<tailnet host> and /home/<user>/lex`},
		{`mail a@example.com on mediabox`, `mail <email> on <host 1>`},
		{`lex 0.0.20 (9a866fe) ffmpeg=7.1.5 at 14:35:21 from 127.0.0.1`, `lex 0.0.20 (9a866fe) ffmpeg=7.1.5 at 14:35:21 from 127.0.0.1`},
		{`client 2001:db8::1 and fe80::1`, `client <ip 1> and <lan ip 2>`},
		// A user name inside another word is left alone.
		{`alexandrax and xalexandra`, `alexandrax and xalexandra`},
	}
	for _, c := range cases {
		if got := r.Line(c.in); got != c.want {
			t.Errorf("\n in   %s\n got  %s\n want %s", c.in, got, c.want)
		}
	}
	// Placeholders stay the same across lines.
	if a, b := r.Line("from 8.8.8.8"), r.Line("again 8.8.8.8"); !strings.HasSuffix(b, strings.TrimPrefix(a, "from ")) {
		t.Errorf("%q vs %q", a, b)
	}
}
