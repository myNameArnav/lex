package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"lex/internal/store"
	"lex/internal/tv"
)

// Live TV: the guide and tuning in. Times are Unix milliseconds; serverTime
// lets the player work out how far into a program it joins without trusting
// the device's clock.

// liveTV answers 404 while Live TV is turned off (Settings › Live TV).
func (s *Server) liveTV(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.St.Config().LiveTV {
			writeErr(w, 404, "Live TV is turned off")
			return
		}
		next(w, r)
	}
}

type programView struct {
	Item  *store.Item `json:"item"`
	Start int64       `json:"start"`
	End   int64       `json:"end"`
}

type channelView struct {
	tv.Channel
	// Live: a channel from the internet, with its logo.
	Live     bool          `json:"live,omitempty"`
	Logo     string        `json:"logo,omitempty"`
	Programs []programView `json:"programs"`
}

// guideWindow is how far ahead the guide looks by default, and
// guidePrograms how many programs it shows per channel then. With ?from=
// and ?to= (Unix ms) it answers every program in that window instead, up to
// maxGuideWindow long, starting no more than a day back.
const (
	guideWindow    = 3 * time.Hour
	guidePrograms  = 4
	maxGuideWindow = 24 * time.Hour
)

// guideRange reads ?from= and ?to=; ok is false when they aren't given.
func guideRange(r *http.Request, now time.Time) (from, to time.Time, ok bool) {
	f, err1 := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	t, err2 := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if err1 != nil || err2 != nil {
		return now, now.Add(guideWindow), false
	}
	from, to = time.UnixMilli(f), time.UnixMilli(t)
	if from.Before(now.Add(-maxGuideWindow)) {
		from = now.Add(-maxGuideWindow)
	}
	if from.After(now.Add(7 * maxGuideWindow)) {
		from = now.Add(7 * maxGuideWindow)
	}
	if !to.After(from) {
		to = from.Add(guideWindow)
	}
	if to.Sub(from) > maxGuideWindow {
		to = from.Add(maxGuideWindow)
	}
	return from, to, true
}

func (s *Server) tvGuide(w http.ResponseWriter, r *http.Request) {
	chs, err := s.TV.Channels()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	now := time.Now()
	from, to, ranged := guideRange(r, now)
	u := userOf(r)
	out := []channelView{}
	for _, ch := range chs {
		if ok, err := s.canWatch(u, ch); err != nil {
			writeErr(w, 500, err.Error())
			return
		} else if !ok {
			continue
		}
		if ch.Stream != nil {
			limit := guidePrograms
			if ranged {
				limit = 0
			}
			out = append(out, channelView{Channel: ch, Live: true, Logo: ch.Stream.Logo, Programs: s.streamPrograms(ch, from, to, limit)})
			continue
		}
		var progs []programView
		sched, err := s.TV.Programs(ch, from, to)
		if err == nil {
			limit := guidePrograms
			if ranged {
				limit = len(sched)
			}
			progs, err = s.programViews(sched, u, limit)
		}
		if errors.Is(err, errHiddenChannel) {
			continue
		}
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if len(progs) > 0 {
			out = append(out, channelView{Channel: ch, Programs: progs})
		}
	}
	writeJSON(w, map[string]any{"channels": out, "serverTime": now.UnixMilli(), "from": from.UnixMilli(), "to": to.UnixMilli()})
}

// tvChannel answers what's on a channel: at ?at= (Unix ms; the end of the
// program that just finished, so the player doesn't tune back into it when
// its own clock runs ahead, or a file ended before its slot), or now. A time
// in the past means now.
func (s *Server) tvChannel(w http.ResponseWriter, r *http.Request) {
	ch, ok, err := s.TV.Channel(r.PathValue("id"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	u := userOf(r)
	if ok {
		if ok, err = s.canWatch(u, ch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if !ok {
		writeErr(w, 404, "no such channel")
		return
	}
	now := time.Now()
	if ch.Stream != nil {
		s.tuneStream(w, ch, now)
		return
	}
	at := now
	if ms, err := strconv.ParseInt(r.URL.Query().Get("at"), 10, 64); err == nil {
		if t := time.UnixMilli(ms); t.After(now) && t.Before(now.Add(24*time.Hour)) {
			at = t
		}
	}
	// A few spare programs: some may have left the library.
	var progs []programView
	sched, err := s.TV.Next(ch, at, 6)
	if err == nil {
		progs, err = s.programViews(sched, u, 2)
	}
	if errors.Is(err, errHiddenChannel) {
		writeErr(w, 404, "no such channel")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(progs) == 0 {
		writeErr(w, 404, "nothing to air on this channel")
		return
	}
	out := map[string]any{"channel": ch, "now": progs[0], "serverTime": now.UnixMilli()}
	if len(progs) > 1 {
		out["next"] = progs[1]
	}
	writeJSON(w, out)
}

// errHiddenChannel: the channel airs something from a library the user
// can't see.
var errHiddenChannel = errors.New("channel airs a library the user can't see")

// canWatch reports whether u may see channel ch: every library it airs
// from must be one they can see. Every viewer gets the same schedule, so a
// channel can't leave out what one of them can't see; it's hidden instead.
func (s *Server) canWatch(u *store.User, ch tv.Channel) (bool, error) {
	if u.IsAdmin || u.AllLibraries {
		return true, nil
	}
	libs, err := s.TV.Libraries(ch)
	if err != nil {
		return false, err
	}
	for _, l := range libs {
		if !u.CanAccess(l) {
			return false, nil
		}
	}
	return true, nil
}

// programViews adds the items to programs, up to limit; a program whose
// title has left the library since the day was scheduled is skipped. A
// program from a library u can't see hides the channel (errHiddenChannel),
// rather than shifting what's on now.
func (s *Server) programViews(progs []tv.Program, u *store.User, limit int) ([]programView, error) {
	ids := make([]int64, len(progs))
	for i, p := range progs {
		ids[i] = p.ItemID
	}
	items, err := s.St.ItemsForUser(ids, u.ID)
	if err != nil {
		return nil, err
	}
	out := []programView{}
	for _, p := range progs {
		it := items[p.ItemID]
		if it == nil {
			// Gone from the library, or hidden from this user?
			if gone, err := s.St.Item(p.ItemID); err == nil && !u.CanAccess(gone.LibraryID) {
				return nil, errHiddenChannel
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			continue
		}
		if len(out) < limit {
			out = append(out, programView{Item: it, Start: p.Start.UnixMilli(), End: p.End.UnixMilli()})
		}
	}
	return out, nil
}
