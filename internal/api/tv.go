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
	Programs []programView `json:"programs"`
}

// guideWindow is how far ahead the guide looks, and guidePrograms how many
// programs it shows per channel at most.
const (
	guideWindow   = 3 * time.Hour
	guidePrograms = 4
)

func (s *Server) tvGuide(w http.ResponseWriter, r *http.Request) {
	chs, err := s.TV.Channels()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	now := time.Now()
	u := userOf(r)
	out := []channelView{}
	for _, ch := range chs {
		if ok, err := s.canWatch(u, ch); err != nil {
			writeErr(w, 500, err.Error())
			return
		} else if !ok {
			continue
		}
		var progs []programView
		sched, err := s.TV.Programs(ch, now, now.Add(guideWindow))
		if err == nil {
			progs, err = s.programViews(sched, u, guidePrograms)
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
	writeJSON(w, map[string]any{"channels": out, "serverTime": now.UnixMilli()})
}

// tvAutomatic lists the automatic channels for Settings › Live TV, including
// the ones turned off, and answers while Live TV is off so it can be set up
// first.
func (s *Server) tvAutomatic(w http.ResponseWriter, r *http.Request) {
	chs, err := s.TV.Automatic()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type auto struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := []auto{}
	for _, c := range chs {
		out = append(out, auto{c.ID, c.Name})
	}
	writeJSON(w, out)
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
