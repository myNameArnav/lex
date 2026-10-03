package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"lex/internal/store"
	"lex/internal/sysstats"
)

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.St.Config())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	old := s.St.Config()
	c := old
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, "bad config: "+err.Error())
		return
	}
	c, err := s.St.SaveConfig(c)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Log.Infof("settings updated by %s", userOf(r).Name)
	if old.CacheEnabled && !c.CacheEnabled {
		s.Cache.OnDisable()
	}
	if c.CacheDir != old.CacheDir {
		s.Cache.Reconcile()
	}
	if c.IntroDetect && !old.IntroDetect {
		s.Intro.Trigger()
	}
	if c.TrickplayEnabled && !old.TrickplayEnabled {
		s.Trick.Trigger()
	}
	// New metadata credentials: retry everything that failed.
	if c.TMDBKey != old.TMDBKey || c.EnableTMDB != old.EnableTMDB || c.MetadataLanguage != old.MetadataLanguage ||
		c.RadarrURL != old.RadarrURL || c.RadarrKey != old.RadarrKey || c.EnableTVmaze != old.EnableTVmaze {
		s.St.DB().Exec(`UPDATE items SET meta_status=0 WHERE meta_status IN (2,3) OR (? AND meta_locked=0)`, c.TMDBKey != "" && old.TMDBKey == "")
		s.Agent.Trigger()
	}
	writeJSON(w, c)
}

func (s *Server) adminInfo(w http.ResponseWriter, r *http.Request) {
	cfg := s.St.Config()
	writeJSON(w, map[string]any{
		"version":    s.Version,
		"go":         runtime.Version(),
		"dataDir":    s.DataDir,
		"ffmpeg":     s.FF,
		"imageCache": sysstats.DirSize(s.Images.Dir),
		"subsCache":  sysstats.DirSize(filepath.Join(s.DataDir, "subs")),
		"dbSize":     fileSize(filepath.Join(s.DataDir, "lex.db")) + fileSize(filepath.Join(s.DataDir, "lex.db-wal")),
		"webhookUrl": "/api/webhook/scan?token=" + cfg.WebhookToken,
	})
}

func fileSize(p string) int64 {
	if st, err := os.Stat(p); err == nil {
		return st.Size()
	}
	return 0
}

type libReq struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}

func cleanPaths(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, errBad("paths must be absolute: " + p)
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return nil, errBad("not a readable folder: " + p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out, nil
}

// pathWithin reports whether p is base or a folder inside it.
func pathWithin(p, base string) bool {
	return p == base || base == "/" || strings.HasPrefix(p, base+"/")
}

// checkLibraryPaths rejects a folder listed twice, a folder and one of its
// sub-folders in the same library, and a folder already used by another
// library (nesting across libraries is allowed). self is the library being
// edited (0 when creating). Paths must already be cleaned.
func checkLibraryPaths(paths []string, libs []store.Library, self int64) error {
	if len(paths) == 0 {
		return errBad("add at least one folder")
	}
	for i, p := range paths {
		for _, q := range paths[i+1:] {
			switch {
			case p == q:
				return errBad(fmt.Sprintf("%q is listed twice", p))
			case pathWithin(q, p):
				return errBad(fmt.Sprintf("%q is inside %q; add only one of them", q, p))
			case pathWithin(p, q):
				return errBad(fmt.Sprintf("%q is inside %q; add only one of them", p, q))
			}
		}
	}
	for _, l := range libs {
		if l.ID == self {
			continue
		}
		for _, q := range l.Paths {
			if slices.Contains(paths, filepath.Clean(q)) {
				return errBad(fmt.Sprintf("%q is already in library %q", filepath.Clean(q), l.Name))
			}
		}
	}
	return nil
}

// validLibrary checks a create or update request, naming the field at fault.
func (s *Server) validLibrary(req *libReq, self int64) ([]string, error) {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return nil, errBad("enter a name")
	}
	if req.Kind != "movies" && req.Kind != "shows" && req.Kind != "mixed" {
		return nil, errBad("type must be movies, shows or mixed")
	}
	paths, err := cleanPaths(req.Paths)
	if err != nil {
		return nil, err
	}
	libs, err := s.St.Libraries()
	if err != nil {
		return nil, err
	}
	return paths, checkLibraryPaths(paths, libs, self)
}

type errBad string

func (e errBad) Error() string { return string(e) }

func (s *Server) createLibrary(w http.ResponseWriter, r *http.Request) {
	var req libReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	paths, err := s.validLibrary(&req, 0)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	lib, err := s.St.CreateLibrary(req.Name, req.Kind, paths)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Log.Infof("library %q created (%s): %v", lib.Name, lib.Kind, lib.Paths)
	s.Scanner.Trigger(lib.ID)
	writeJSON(w, lib)
}

func (s *Server) updateLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req libReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	old, err := s.St.Library(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	paths, err := s.validLibrary(&req, id)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.St.UpdateLibrary(id, req.Name, req.Kind, paths); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Changing the library type changes how every file is classified.
	if old.Kind != req.Kind {
		s.St.DB().Exec(`DELETE FROM items WHERE library_id=?`, id)
	}
	s.Scanner.Trigger(id)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	if err := s.St.DeleteLibrary(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LibraryID int64 `json:"libraryId"`
	}
	readJSON(r, &req)
	s.Scanner.Trigger(req.LibraryID)
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	remux, tx := s.Sess.ActiveJobs()
	writeJSON(w, map[string]any{"scan": s.Scanner.Status(), "metadata": s.Agent.Status(), "intro": s.Intro.Status(), "cache": s.Cache.Status(), "trickplay": s.Trick.Status(), "remuxJobs": remux, "transcodeJobs": tx})
}

func (s *Server) refreshAllMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Missing bool `json:"missingOnly"`
	}
	readJSON(r, &req)
	if req.Missing {
		s.St.DB().Exec(`UPDATE items SET meta_status=0 WHERE meta_status IN (2,3)`)
	} else {
		s.St.DB().Exec(`UPDATE items SET meta_status=0`)
	}
	s.Agent.Trigger()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) metaSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind != "show" {
		kind = "movie"
	}
	year, _ := strconv.Atoi(q.Get("year"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.Agent.Search(ctx, kind, strings.TrimSpace(q.Get("q")), year)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if c == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, c)
}

func (s *Server) refreshItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Agent.Refresh(ctx, id); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) matchItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct{ Provider, ID string }
	if err := readJSON(r, &req); err != nil || req.Provider == "" || req.ID == "" {
		writeErr(w, 400, "provider and id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.Agent.Match(ctx, id, req.Provider, req.ID); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) unmatchItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	if err := s.Agent.Unmatch(id); err != nil {
		notFoundOr500(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) browseFS(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p = "/"
	}
	p = filepath.Clean(p)
	ents, err := os.ReadDir(p)
	if err != nil {
		writeErr(w, 400, fsErr(p, err))
		return
	}
	dirs := []string{}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(p, e.Name())); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	parent := filepath.Dir(p)
	writeJSON(w, map[string]any{"path": p, "parent": parent, "dirs": dirs})
}

// fsErr words a folder-browser failure for people rather than as a Go error.
func fsErr(p string, err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "There's no folder at " + p
	case errors.Is(err, os.ErrPermission):
		return "Lex isn't allowed to read " + p
	}
	if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
		return p + " is a file, not a folder"
	}
	return "Can't open " + p + ": " + err.Error()
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.St.Users()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, users)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Password string
		IsAdmin        bool `json:"isAdmin"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	u, err := s.St.CreateUser(req.Name, req.Password, req.IsAdmin)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, u)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Password string `json:"password"`
		IsAdmin  *bool  `json:"isAdmin"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	// Another admin has to do it, so an admin can't lock themselves out.
	if req.IsAdmin != nil && !*req.IsAdmin && id == userOf(r).ID {
		writeErr(w, 400, "you can't change your own role")
		return
	}
	if req.Password != "" {
		if err := s.St.SetPassword(id, req.Password); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if req.IsAdmin != nil {
		if err := s.St.SetAdmin(id, *req.IsAdmin); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	if id == userOf(r).ID {
		writeErr(w, 400, "you can't delete yourself")
		return
	}
	if err := s.St.DeleteUser(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	t, err := s.St.Tokens()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if t == nil {
		t = []store.TokenInfo{}
	}
	// Mark the caller's own session so the UI can say "This device".
	if tok := s.token(r); len(tok) >= 8 {
		for i := range t {
			t[i].Current = t[i].Prefix == tok[:8]
		}
	}
	writeJSON(w, t)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.St.DeleteTokenPrefix(r.PathValue("prefix")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) statsSystem(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"snapshot": s.Stats.Snapshot()}
	if r.URL.Query().Get("history") == "1" {
		out["history"] = s.Stats.History()
	}
	remux, tx := s.Sess.ActiveJobs()
	out["remuxJobs"], out["transcodeJobs"] = remux, tx
	out["cache"] = s.Cache.Status()
	writeJSON(w, out)
}

func (s *Server) statsSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Sess.Snapshot())
}

func (s *Server) killSession(w http.ResponseWriter, r *http.Request) {
	if !s.Sess.Kill(r.PathValue("id")) {
		writeErr(w, 404, "no such session")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) statsLibrary(w http.ResponseWriter, r *http.Request) {
	st, err := s.St.LibraryStats()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, st)
}

// statsLibraryTitles lists the titles in one Library-stats category, e.g.
// ?dim=hdr&key=HDR10 or ?dim=audio&key=dts.
func (s *Server) statsLibraryTitles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.St.LibraryTitles(q.Get("dim"), q.Get("key"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, list)
}

func (s *Server) statsPlayback(w http.ResponseWriter, r *http.Request) {
	days := qInt(r, "days", 30)
	if days < 1 || days > 3650 {
		days = 30
	}
	tz := qInt(r, "tz", 0) // minutes east of UTC
	if tz < -14*60 || tz > 14*60 {
		tz = 0
	}
	st, err := s.St.PlaybackStats(days, tz)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, st)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	h, err := s.St.History(min(qInt(r, "limit", 100), 1000), qInt(r, "offset", 0))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if h == nil {
		h = []store.HistoryEntry{}
	}
	writeJSON(w, h)
}

func (s *Server) clearHistory(w http.ResponseWriter, r *http.Request) {
	if err := s.St.ClearHistory(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Log.Lines())
}

func (s *Server) clearCache(w http.ResponseWriter, r *http.Request) {
	var req struct{ Kind string }
	readJSON(r, &req)
	switch req.Kind {
	case "subs":
		s.Subs.Clear()
	case "media":
		s.Cache.Clear()
	case "images":
		// Remove downloaded/resized images; they're re-fetched on demand.
		ents, _ := os.ReadDir(s.Images.Dir)
		for _, e := range ents {
			os.RemoveAll(filepath.Join(s.Images.Dir, e.Name()))
		}
	default:
		writeErr(w, 400, "kind must be subs, images or media")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
