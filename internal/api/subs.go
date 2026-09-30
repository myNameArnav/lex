package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lex/internal/store"
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

func (s *Server) subClient() *subsearch.Client {
	c := s.St.Config()
	s.subMu.Lock()
	defer s.subMu.Unlock()
	if s.subsClient == nil {
		s.subsClient = &subsearch.Client{}
	}
	s.subsClient.Key, s.subsClient.User, s.subsClient.Pass = strings.TrimSpace(c.OpenSubtitlesKey), c.OpenSubtitlesUser, c.OpenSubtitlesPass
	return s.subsClient
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
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	data, _, err := s.subClient().Download(ctx, req.FileID)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	dir := filepath.Join(s.DataDir, "subs", "downloaded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.srt", f.ID, req.FileID))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	title := strings.TrimSpace(req.Release)
	if len(title) > 80 {
		title = title[:80] + "…"
	}
	d := &store.DownloadedSub{FileID: f.ID, Language: lang1to3(req.Language), Title: title, Path: path, Provider: "opensubtitles", HearingImpaired: req.HearingImpaired}
	if err := s.St.AddDownloadedSub(d); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Log.Infof("subtitles: downloaded %s (%s) for %s", title, d.Language, filepath.Base(f.Path))
	st := s.downloadedStreams(f.ID)
	for _, x := range st {
		if x.Index == store.DownloadedSubBase+int(d.ID) {
			writeJSON(w, x)
			return
		}
	}
	writeJSON(w, map[string]int{"index": store.DownloadedSubBase + int(d.ID)})
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
	os.Remove(d.Path)
	s.St.DeleteDownloadedSub(d.ID)
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
		writeErr(w, 500, err.Error())
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
		writeErr(w, 500, err.Error())
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
