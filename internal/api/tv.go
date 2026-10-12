package api

import (
	"net/http"
	"strconv"
	"time"

	"lex/internal/store"
	"lex/internal/tv"
)

// Live TV: the guide and tuning in. Times are Unix milliseconds; serverTime
// lets the player work out how far into a program it joins without trusting
// the device's clock.

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
	out := []channelView{}
	for _, ch := range chs {
		var progs []programView
		sched, err := s.TV.Programs(ch, now, now.Add(guideWindow))
		if err == nil {
			progs, err = s.programViews(sched, userOf(r).ID, guidePrograms)
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
		progs, err = s.programViews(sched, userOf(r).ID, 2)
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

// programViews adds the items to programs, up to limit; a program whose
// title has left the library since the day was scheduled is skipped.
func (s *Server) programViews(progs []tv.Program, uid int64, limit int) ([]programView, error) {
	ids := make([]int64, len(progs))
	for i, p := range progs {
		ids[i] = p.ItemID
	}
	items, err := s.St.ItemsForUser(ids, uid)
	if err != nil {
		return nil, err
	}
	out := []programView{}
	for _, p := range progs {
		if it := items[p.ItemID]; it != nil && len(out) < limit {
			out = append(out, programView{Item: it, Start: p.Start.UnixMilli(), End: p.End.UnixMilli()})
		}
	}
	return out, nil
}
