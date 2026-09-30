package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lex/internal/store"
	"lex/internal/stream"
	"lex/internal/subsearch"
)

// ISO 639-2/B (what probes and prefs use) <-> ISO 639-1 (OpenSubtitles).
var lang3to1 = map[string]string{
	"eng": "en", "jpn": "ja", "spa": "es", "fre": "fr", "ger": "de", "ita": "it", "hin": "hi", "rus": "ru", "por": "pt-pt",
	"chi": "zh-cn", "kor": "ko", "ara": "ar", "dut": "nl", "swe": "sv", "pol": "pl", "tur": "tr", "tam": "ta", "tel": "te",
	"nor": "no", "dan": "da", "fin": "fi", "ukr": "uk", "heb": "he", "tha": "th", "vie": "vi", "ind": "id", "may": "ms",
	"cze": "cs", "gre": "el", "hun": "hu", "rum": "ro",
}

func lang1to3(l string) string {
	l = strings.ToLower(l)
	for k, v := range lang3to1 {
		if v == l || strings.SplitN(v, "-", 2)[0] == l {
			return k
		}
	}
	return l
}

// subClient returns the OpenSubtitles client for the current settings.
// Clients are never modified once handed out (requests use them without
// holding subMu); a settings change swaps in a new one instead.
func (s *Server) subClient() *subsearch.Client {
	c := s.St.Config()
	key, user, pass := strings.TrimSpace(c.OpenSubtitlesKey), c.OpenSubtitlesUser, c.OpenSubtitlesPass
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if cl := s.subsClient; cl == nil || cl.Key != key || cl.User != user || cl.Pass != pass {
		s.subsClient = subsearch.New(key, user, pass)
	}
	return s.subsClient
}

// subFail logs an internal error and sends a generic 500 (the detail can
// name server paths).
func (s *Server) subFail(w http.ResponseWriter, what string, err error) {
	s.Log.Errorf("subtitles: %s: %v", what, err)
	writeErr(w, 500, "subtitle error; see admin logs")
}

// downloadedStreams lists downloaded subtitles as external streams.
func (s *Server) downloadedStreams(fileID int64) []store.Stream {
	subs, _ := s.St.DownloadedSubs(fileID)
	var out []store.Stream
	for _, d := range subs {
		title := "Downloaded"
		if d.Title != "" {
			title = d.Title
		}
		out = append(out, store.Stream{Index: store.DownloadedSubBase + int(d.ID), Type: "subtitle", Codec: "subrip", TextSub: true, External: true,
			Language: d.Language, Title: title, Downloaded: true})
	}
	return out
}

func (s *Server) subSearch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	it, err := s.St.Item(f.ItemID)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = "eng"
	}
	q := subsearch.Query{Language: lang3to1[lang]}
	if q.Language == "" {
		q.Language = lang
	}
	if h, err := subsearch.Hash(s.Cache.Resolve(f)); err == nil {
		q.Hash = h
	}
	ids := it.ProviderIDs
	switch it.Kind {
	case "episode":
		q.Season, q.Episode = it.Season, it.Episode
		if show, err := s.St.Item(it.ShowID); err == nil {
			q.ParentIMDB, q.ParentTMDB = show.ProviderIDs["imdb"], show.ProviderIDs["tmdb"]
			q.Title, q.Year = show.Title, show.Year
		}
	default:
		q.IMDB, q.TMDB = ids["imdb"], ids["tmdb"]
		q.Title, q.Year = it.Title, it.Year
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, err := s.subClient().Search(ctx, q)
	if err != nil {
		code := 502
		if err == subsearch.ErrNoKey {
			code = 400
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, res)
}

func (s *Server) subDownload(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	var req struct {
		FileID          int64  `json:"fileId"`
		Language        string `json:"language"`
		Release         string `json:"release"`
		HearingImpaired bool   `json:"hearingImpaired"`
	}
	if err := readJSON(r, &req); err != nil || req.FileID <= 0 {
		writeErr(w, 400, "fileId required")
		return
	}
	dir := filepath.Join(s.DataDir, "subs", "downloaded")
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.srt", f.ID, req.FileID))
	// Already downloaded for this file: hand out the existing track rather
	// than spending quota on a second copy.
	if d, err := s.St.DownloadedSubByPath(f.ID, path); err == nil {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			s.writeDownloaded(w, f.ID, d.ID)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	data, _, err := s.subClient().Download(ctx, req.FileID)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.subFail(w, "download", err)
		return
	}
	if err := stream.WriteFileAtomic(path, data); err != nil {
		s.subFail(w, "download", err)
		return
	}
	title := strings.TrimSpace(req.Release)
	if len(title) > 80 {
		title = title[:80] + "…"
	}
	d := &store.DownloadedSub{FileID: f.ID, Language: lang1to3(req.Language), Title: title, Path: path, Provider: "opensubtitles",
		HearingImpaired: req.HearingImpaired, UserID: userOf(r).ID}
	if _, err := s.St.AddDownloadedSub(d); err != nil {
		s.subFail(w, "download", err)
		return
	}
	s.Log.Infof("subtitles: downloaded %s (%s) for %s", title, d.Language, filepath.Base(f.Path))
	s.writeDownloaded(w, f.ID, d.ID)
}

// writeDownloaded responds with the stream entry for a downloaded subtitle.
func (s *Server) writeDownloaded(w http.ResponseWriter, fileID, id int64) {
	st := s.downloadedStreams(fileID)
	for _, x := range st {
		if x.Index == store.DownloadedSubBase+int(id) {
			writeJSON(w, x)
			return
		}
	}
	writeJSON(w, map[string]int{"index": store.DownloadedSubBase + int(id)})
}

func (s *Server) subDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < store.DownloadedSubBase {
		writeErr(w, 400, "only downloaded subtitles can be deleted")
		return
	}
	d, err := s.St.DownloadedSub(int64(idx - store.DownloadedSubBase))
	if err != nil || d.FileID != id {
		writeErr(w, 404, "not found")
		return
	}
	// Downloaded subtitles are shared by everyone who can see the file, so
	// only whoever fetched one (or an admin) may remove it.
	if u := userOf(r); !u.IsAdmin && d.UserID != u.ID {
		writeErr(w, 403, "only the user who downloaded this subtitle or an admin can delete it")
		return
	}
	free, err := s.St.DeleteDownloadedSub(d.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, 404, "not found")
		return
	} else if err != nil {
		s.subFail(w, "delete", err)
		return
	}
	if free {
		os.Remove(d.Path)
	}
	if s.Subs != nil {
		s.Subs.Forget(id, idx)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// fonts lists the fonts attached to a file (for ASS rendering).
func (s *Server) fonts(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	_, names, err := s.Subs.Fonts(r.Context(), f)
	if err != nil {
		s.subFail(w, "fonts", err)
		return
	}
	urls := []string{}
	for _, n := range names {
		urls = append(urls, fmt.Sprintf("/api/files/%d/fonts/%s", f.ID, n))
	}
	writeJSON(w, urls)
}

func (s *Server) font(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	f, err := s.St.File(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	dir, names, err := s.Subs.Fonts(r.Context(), f)
	if err != nil {
		s.subFail(w, "fonts", err)
		return
	}
	name := r.PathValue("name")
	for _, n := range names {
		if n == name {
			w.Header().Set("Cache-Control", "private, max-age=2592000")
			http.ServeFile(w, r, filepath.Join(dir, n))
			return
		}
	}
	writeErr(w, 404, "no such font")
}
