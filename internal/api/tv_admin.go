package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"lex/internal/store"
	"lex/internal/tv"
)

// Settings › Live TV: the lineup (every channel, in order, on or off), the
// admin's own channels, and a preview of a channel being edited. Each change
// saves on its own; these answer while Live TV is off, so it can be set up
// first.

type tvLineupChannel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"name"`
	Off    bool   `json:"off"`
	// Source is "custom" (the admin's), "stream", "all", "library" or "genre".
	Source string `json:"source"`
	// Titles counts the movies and shows it can air.
	Titles int `json:"titles"`
	// Custom is the admin's channel, to edit.
	Custom *store.TVChannel `json:"custom,omitempty"`
	// Stream is a live channel from the internet, to edit.
	Stream *store.TVStream `json:"stream,omitempty"`
}

type tvLineup struct {
	Enabled  bool              `json:"enabled"`
	Channels []tvLineupChannel `json:"channels"`
	Sources  []store.TVSource  `json:"sources"`
	// Removed are the automatic channels taken out of the lineup, to
	// restore.
	Removed []tvRemoved `json:"removed"`
	// GuideError says why a source's guide couldn't be read.
	GuideError string `json:"guideError,omitempty"`
	// Saved is the id of the channel a create made.
	Saved string `json:"saved,omitempty"`
}

type tvRemoved struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) lineup() (tvLineup, error) {
	cfg := s.St.Config()
	chs, err := s.TV.Lineup()
	if err != nil {
		return tvLineup{}, err
	}
	out := tvLineup{Enabled: cfg.LiveTV, Channels: []tvLineupChannel{}, Sources: cfg.TVSources}
	if _, err := s.TV.GuideStatus(); err != nil {
		out.GuideError = err.Error()
	}
	out.Removed = []tvRemoved{}
	if len(cfg.TVRemoved) > 0 {
		auto, err := s.TV.Automatic()
		if err != nil {
			return tvLineup{}, err
		}
		for _, a := range auto {
			if slices.Contains(cfg.TVRemoved, a.ID) {
				out.Removed = append(out.Removed, tvRemoved{a.ID, a.Name})
			}
		}
	}
	for _, ch := range chs {
		n, err := s.TV.Titles(ch)
		if err != nil {
			return tvLineup{}, err
		}
		v := tvLineupChannel{ID: ch.ID, Number: ch.Number, Name: ch.Name, Off: ch.Off, Titles: n}
		switch {
		case strings.HasPrefix(ch.ID, "custom-"):
			v.Source = "custom"
			if i := slices.IndexFunc(cfg.TVChannels, func(c store.TVChannel) bool { return "custom-"+c.ID == ch.ID }); i >= 0 {
				c := cfg.TVChannels[i]
				v.Custom = &c
			}
		case ch.Stream != nil:
			v.Source = "stream"
			st := *ch.Stream
			v.Stream = &st
		case ch.ID == "all":
			v.Source = "all"
		case strings.HasPrefix(ch.ID, "lib-"):
			v.Source = "library"
		default:
			v.Source = "genre"
		}
		out.Channels = append(out.Channels, v)
	}
	return out, nil
}

func (s *Server) writeLineup(w http.ResponseWriter, saved string) {
	l, err := s.lineup()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	l.Saved = saved
	writeJSON(w, l)
}

func (s *Server) tvAdmin(w http.ResponseWriter, r *http.Request) {
	s.writeLineup(w, "")
}

// tvLineupEdit is a change to the lineup; what it leaves out stays as it is.
type tvLineupEdit struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Order lists channel ids, first to last.
	Order []string `json:"order,omitempty"`
	// Off lists the channels turned off.
	Off []string `json:"off,omitempty"`
	// Removed lists the automatic channels taken out of the lineup.
	Removed []string `json:"removed,omitempty"`
}

func (s *Server) tvAdminEdit(w http.ResponseWriter, r *http.Request) {
	var in tvLineupEdit
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "bad request: "+err.Error())
		return
	}
	_, err := s.St.UpdateConfig(func(c *store.Config) error {
		if in.Enabled != nil {
			c.LiveTV = *in.Enabled
		}
		if in.Order != nil {
			c.TVOrder = in.Order
		}
		if in.Off != nil {
			c.TVHidden = in.Off
		}
		if in.Removed != nil {
			c.TVRemoved = in.Removed
		}
		return nil
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.writeLineup(w, "")
}

// errBadChannel is a channel that can't be saved; its message says why.
type errBadChannel string

func (e errBadChannel) Error() string { return string(e) }

func checkChannel(ch store.TVChannel) error {
	if ch.Name == "" {
		return errBadChannel("Give the channel a name")
	}
	if len([]rune(ch.Name)) > 60 {
		return errBadChannel("Keep the channel name to 60 characters")
	}
	if ch.Kind == "picked" && len(ch.Titles) == 0 {
		return errBadChannel("Pick at least one title")
	}
	return nil
}

func (s *Server) writeChannelErr(w http.ResponseWriter, err error) {
	var bad errBadChannel
	switch {
	case errors.As(err, &bad):
		writeErr(w, 400, bad.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "no such channel")
	default:
		writeErr(w, 500, err.Error())
	}
}

// tvChannelCreate adds an admin's channel at the end of the lineup.
func (s *Server) tvChannelCreate(w http.ResponseWriter, r *http.Request) {
	var ch store.TVChannel
	if err := readJSON(r, &ch); err != nil {
		writeErr(w, 400, "bad channel: "+err.Error())
		return
	}
	ch = ch.Clean()
	if err := checkChannel(ch); err != nil {
		s.writeChannelErr(w, err)
		return
	}
	// The lineup so far becomes the saved order, so the new channel goes
	// last rather than with the admin's others.
	chs, err := s.TV.Lineup()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_, err = s.St.UpdateConfig(func(c *store.Config) error {
		ch.ID = store.RandomToken(4)
		for slices.ContainsFunc(c.TVChannels, func(o store.TVChannel) bool { return o.ID == ch.ID }) {
			ch.ID = store.RandomToken(4)
		}
		order := []string{}
		for _, o := range chs {
			order = append(order, o.ID)
		}
		c.TVOrder = append(order, "custom-"+ch.ID)
		c.TVChannels = append(c.TVChannels, ch)
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.Log.Infof("Live TV channel %q added by %s", ch.Name, userOf(r).Name)
	s.writeLineup(w, "custom-"+ch.ID)
}

func (s *Server) tvChannelUpdate(w http.ResponseWriter, r *http.Request) {
	var ch store.TVChannel
	if err := readJSON(r, &ch); err != nil {
		writeErr(w, 400, "bad channel: "+err.Error())
		return
	}
	ch = ch.Clean()
	ch.ID = r.PathValue("id")
	if err := checkChannel(ch); err != nil {
		s.writeChannelErr(w, err)
		return
	}
	_, err := s.St.UpdateConfig(func(c *store.Config) error {
		i := slices.IndexFunc(c.TVChannels, func(o store.TVChannel) bool { return o.ID == ch.ID })
		if i < 0 {
			return store.ErrNotFound
		}
		c.TVChannels[i] = ch
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.writeLineup(w, "")
}

func (s *Server) tvChannelDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var name string
	_, err := s.St.UpdateConfig(func(c *store.Config) error {
		i := slices.IndexFunc(c.TVChannels, func(o store.TVChannel) bool { return o.ID == id })
		if i < 0 {
			return store.ErrNotFound
		}
		name = c.TVChannels[i].Name
		// Saving drops it from the order and the channels turned off.
		c.TVChannels = slices.Delete(c.TVChannels, i, i+1)
		return nil
	})
	if err != nil {
		s.writeChannelErr(w, err)
		return
	}
	s.Log.Infof("Live TV channel %q removed by %s", name, userOf(r).Name)
	s.writeLineup(w, "")
}

// previewPrograms is how many programs a preview shows.
const previewPrograms = 5

// tvPreview answers what a channel being edited would air: how many titles
// it has and what's on from now. A saved channel's id makes the
// preview match what it airs today, while its choices are unchanged.
func (s *Server) tvPreview(w http.ResponseWriter, r *http.Request) {
	var ch store.TVChannel
	if err := readJSON(r, &ch); err != nil {
		writeErr(w, 400, "bad channel: "+err.Error())
		return
	}
	ch = ch.Clean()
	id := "custom-new"
	if ch.ID != "" {
		id = "custom-" + ch.ID
	}
	// A few spare programs: a title may have left the library since.
	n, sched, err := s.TV.Preview(id, tv.Filter(ch), time.Now(), previewPrograms+3)
	var progs []programView
	if err == nil {
		progs, err = s.programViews(sched, userOf(r), previewPrograms)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"titles": n, "programs": progs})
}
