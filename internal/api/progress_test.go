package api

import (
	"testing"

	"lex/internal/store"
)

// A watched title stopped partway through a rewatch keeps its resume point:
// it's on Continue Watching, and removing it from there leaves it watched.
func TestRewatchResumePoint(t *testing.T) {
	s, h := cacheServer(t)
	token := tvLibrary(t, s)
	u, _ := s.St.TokenUser(token)
	s.St.MarkPlayed(u.ID, 1, true)
	s.St.SaveProgress(u.ID, 1, 4400)

	continueRow := func() []*store.Item {
		t.Helper()
		var home struct{ Rows []homeRow }
		if code := getJSON(t, h, token, "/api/home", &home); code != 200 {
			t.Fatalf("home: %d", code)
		}
		for _, r := range home.Rows {
			if r.ID == "continue" {
				return r.Items
			}
		}
		return nil
	}
	cw := continueRow()
	if len(cw) != 1 || cw[0].ID != 1 || cw[0].UserData.Position != 4400 || !cw[0].UserData.Played {
		t.Fatalf("continue watching = %+v, want Heat at 4400, watched", cw)
	}

	if w := adminRequest(t, h, token, "DELETE", "/api/items/1/progress", ""); w.Code != 200 {
		t.Fatalf("clear progress: %d %s", w.Code, w.Body)
	}
	if cw := continueRow(); len(cw) != 0 {
		t.Fatalf("continue watching after removal = %+v", cw)
	}
	ud, _ := s.St.GetUserData(u.ID, 1)
	if !ud.Played || ud.PlayCount != 1 || ud.Position != 0 {
		t.Fatalf("user data after removal = %+v, want watched once, no position", ud)
	}
}
