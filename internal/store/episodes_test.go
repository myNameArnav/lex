package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// The reference versions below are the ones NextUp, AdjacentEpisodes and
// ShowNextEpisode replaced: they walk every episode of a show in Go.

func nextUpReference(t *testing.T, st *Store, uid int64, limit int) []int64 {
	t.Helper()
	rows, err := st.db.Query(`SELECT e.show_id, MAX(ud.last_played) lp FROM user_data ud JOIN items e ON e.id=ud.item_id
		WHERE ud.user_id=? AND e.kind='episode' AND (ud.played=1 OR ud.position>0) GROUP BY e.show_id ORDER BY lp DESC, e.show_id LIMIT ?`, uid, limit*2)
	if err != nil {
		t.Fatal(err)
	}
	var shows []int64
	for rows.Next() {
		var id, lp int64
		rows.Scan(&id, &lp)
		shows = append(shows, id)
	}
	rows.Close()
	var out []int64
	for _, sh := range shows {
		eps, err := st.ShowEpisodes(sh, uid)
		if err != nil {
			t.Fatal(err)
		}
		lastIdx := -1
		var lastTime int64
		for i, e := range eps {
			if e.UserData.LastPlayed >= lastTime && (e.UserData.Played || e.UserData.Position > 0) {
				lastTime = e.UserData.LastPlayed
				lastIdx = i
			}
		}
		if lastIdx < 0 || !eps[lastIdx].UserData.Played {
			continue
		}
		for j := lastIdx + 1; j < len(eps); j++ {
			if eps[j].Season != 0 && !eps[j].UserData.Played {
				out = append(out, eps[j].ID)
				break
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func adjacentReference(t *testing.T, st *Store, ep *Item, uid int64) (prev, next int64) {
	t.Helper()
	eps, err := st.ShowEpisodes(ep.ShowID, uid)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range eps {
		if e.ID == ep.ID {
			if i > 0 {
				prev = eps[i-1].ID
			}
			if i+1 < len(eps) {
				next = eps[i+1].ID
			}
		}
	}
	return
}

func showNextReference(t *testing.T, st *Store, showID, uid int64) int64 {
	t.Helper()
	eps, err := st.ShowEpisodes(showID, uid)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.Season > 0 && !e.UserData.Played {
			return e.ID
		}
	}
	if len(eps) > 0 {
		return eps[0].ID
	}
	return 0
}

func itemIDs(items []*Item) []int64 {
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func idOf(it *Item) int64 {
	if it == nil {
		return 0
	}
	return it.ID
}

type episodeFixture struct {
	st   *Store
	t    *testing.T
	next int64
}

func newEpisodeFixture(t *testing.T) *episodeFixture {
	st := testStore(t)
	for _, name := range []string{"one", "two"} {
		if _, err := st.CreateUser(name, "test-password-123", false); err != nil {
			t.Fatal(err)
		}
	}
	f := &episodeFixture{st: st, t: t, next: 1}
	f.exec(`INSERT INTO libraries(id,name,kind,paths,created_at) VALUES(1,'TV','shows','[]',0)`)
	return f
}

func (f *episodeFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.st.db.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

// show adds a show with the given (season, episode) episodes, each in its
// season, and returns the show id and the episode ids in that order.
func (f *episodeFixture) show(eps ...[2]int) (int64, []int64) {
	f.t.Helper()
	sh := f.next
	f.next++
	f.exec(`INSERT INTO items(id,library_id,kind,show_id,title,sort_title,added_at,updated_at) VALUES(?,1,'show',0,?,?,0,0)`, sh, fmt.Sprint("Show ", sh), fmt.Sprint("show ", sh))
	seasons := map[int]int64{}
	var ids []int64
	for _, e := range eps {
		sid, ok := seasons[e[0]]
		if !ok {
			sid = f.next
			f.next++
			seasons[e[0]] = sid
			f.exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,added_at,updated_at) VALUES(?,1,'season',?,?,'S','s',?,0,0)`, sid, sh, sh, e[0])
		}
		id := f.next
		f.next++
		f.exec(`INSERT INTO items(id,library_id,kind,parent_id,show_id,title,sort_title,season,episode,added_at,updated_at) VALUES(?,1,'episode',?,?,'E','e',?,?,0,0)`, id, sid, sh, e[0], e[1])
		ids = append(ids, id)
	}
	return sh, ids
}

func (f *episodeFixture) watch(uid, item int64, played bool, position float64, at int64) {
	f.t.Helper()
	f.exec(`INSERT INTO user_data(user_id,item_id,played,position,last_played) VALUES(?,?,?,?,?)
		ON CONFLICT(user_id,item_id) DO UPDATE SET played=excluded.played,position=excluded.position,last_played=excluded.last_played`, uid, item, played, position, at)
}

func (f *episodeFixture) nextUp(uid int64, limit int) []int64 {
	f.t.Helper()
	items, err := f.st.NextUp(uid, limit)
	if err != nil {
		f.t.Fatal(err)
	}
	return itemIDs(items)
}

func TestNextUpEdgeCases(t *testing.T) {
	f := newEpisodeFixture(t)
	ep := func(s, e int) [2]int { return [2]int{s, e} }

	// A: watched S1E1, a special airs between it and S1E2: the special is skipped.
	_, a := f.show(ep(1, 1), ep(0, 1), ep(1, 2))
	f.watch(1, a[0], true, 0, 10)
	// B: S1E2 is in progress after S1E1 was watched: Continue Watching's.
	_, b := f.show(ep(1, 1), ep(1, 2), ep(1, 3))
	f.watch(1, b[0], true, 0, 20)
	f.watch(1, b[1], false, 300, 21)
	// C: the last episode is watched: nothing is next.
	_, c := f.show(ep(1, 1), ep(1, 2))
	f.watch(1, c[1], true, 0, 30)
	// D: watched out of order: next is after the episode watched last
	// (S2E1), not the earlier unwatched S1E2; S2E2, already watched, is
	// passed over.
	_, d := f.show(ep(1, 1), ep(1, 2), ep(2, 1), ep(2, 2), ep(2, 3))
	f.watch(1, d[0], true, 0, 40)
	f.watch(1, d[3], true, 0, 41)
	f.watch(1, d[2], true, 0, 42)
	// E: two episodes watched at the same second: the later one in episode
	// order counts as last.
	_, e := f.show(ep(1, 1), ep(1, 2), ep(1, 3))
	f.watch(1, e[1], true, 0, 50)
	f.watch(1, e[0], true, 0, 50)
	// F: the last watched episode is a special: the next regular episode
	// after it in order (season 1 sorts after specials).
	_, g := f.show(ep(0, 1), ep(1, 1), ep(1, 2))
	f.watch(1, g[0], true, 0, 60)
	// G: only specials left after the last watched: nothing.
	_, h := f.show(ep(1, 1), ep(0, 5))
	f.watch(1, h[0], true, 0, 5)
	// Another user's activity doesn't count.
	_, o := f.show(ep(1, 1), ep(1, 2))
	f.watch(2, o[0], true, 0, 999)

	want := []int64{g[1], e[2], d[4], a[2]}
	if got := f.nextUp(1, 20); !slices.Equal(got, want) {
		t.Fatalf("next up = %v, want %v", got, want)
	}
	if got := f.nextUp(1, 2); !slices.Equal(got, want[:2]) {
		t.Fatalf("next up limit 2 = %v, want %v", got, want[:2])
	}
	if got := f.nextUp(2, 20); !slices.Equal(got, []int64{o[1]}) {
		t.Fatalf("user two next up = %v, want [%d]", got, o[1])
	}
	// Next up carries the card's user data and show title.
	items, _ := f.st.NextUp(1, 1)
	if items[0].ShowTitle == "" || items[0].UserData == nil || items[0].UserData.Played {
		t.Fatalf("next up card = %+v", items[0])
	}
}

// The SQL versions agree with the Go walks they replaced on random shows
// with specials, duplicate episode numbers, ties and in-progress episodes.
func TestEpisodeQueriesMatchReference(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			f := newEpisodeFixture(t)
			rng := rand.New(rand.NewPCG(seed, 99))
			var all []int64
			var shows []int64
			for range 12 {
				var eps [][2]int
				for range rng.IntN(12) {
					eps = append(eps, [2]int{rng.IntN(3), 1 + rng.IntN(5)})
				}
				sh, ids := f.show(eps...)
				shows = append(shows, sh)
				all = append(all, ids...)
			}
			for _, id := range all {
				for uid := int64(1); uid <= 2; uid++ {
					switch rng.IntN(4) {
					case 0:
						f.watch(uid, id, true, 0, rng.Int64N(30))
					case 1:
						f.watch(uid, id, false, 100, rng.Int64N(30))
					case 2:
						f.watch(uid, id, false, 0, rng.Int64N(30)) // a reset (unwatched) entry
					}
				}
			}
			for uid := int64(1); uid <= 2; uid++ {
				for _, limit := range []int{1, 3, 20} {
					if got, want := f.nextUp(uid, limit), nextUpReference(t, f.st, uid, limit); !slices.Equal(got, want) {
						t.Fatalf("user %d limit %d: next up = %v, want %v", uid, limit, got, want)
					}
				}
				for _, id := range all {
					ep, err := f.st.Item(id)
					if err != nil {
						t.Fatal(err)
					}
					prev, next, err := f.st.AdjacentEpisodes(ep, uid)
					if err != nil {
						t.Fatal(err)
					}
					wp, wn := adjacentReference(t, f.st, ep, uid)
					if idOf(prev) != wp || idOf(next) != wn {
						t.Fatalf("episode %d: prev/next = %d/%d, want %d/%d", id, idOf(prev), idOf(next), wp, wn)
					}
				}
				for _, sh := range shows {
					got, err := f.st.ShowNextEpisode(sh, uid)
					if err != nil {
						t.Fatal(err)
					}
					if want := showNextReference(t, f.st, sh, uid); idOf(got) != want {
						t.Fatalf("show %d: next episode = %d, want %d", sh, idOf(got), want)
					}
				}
			}
		})
	}
}

func TestAdjacentEpisodesCarryUserData(t *testing.T) {
	f := newEpisodeFixture(t)
	_, eps := f.show([2]int{1, 1}, [2]int{1, 2}, [2]int{1, 3})
	f.watch(1, eps[2], false, 42, 1)
	ep, _ := f.st.Item(eps[1])
	prev, next, err := f.st.AdjacentEpisodes(ep, 1)
	if err != nil || idOf(prev) != eps[0] || idOf(next) != eps[2] {
		t.Fatalf("prev/next = %d/%d (%v)", idOf(prev), idOf(next), err)
	}
	if next.UserData == nil || next.UserData.Position != 42 || next.ShowTitle == "" {
		t.Fatalf("next = %+v", next)
	}
	first, _ := f.st.Item(eps[0])
	if prev, _, _ := f.st.AdjacentEpisodes(first, 1); prev != nil {
		t.Fatalf("first episode has a previous one: %d", prev.ID)
	}
}
