package api

import (
	"net/http"
	"sort"

	"lex/internal/store"
	"lex/internal/stream"
)

// compatReq is what the browser reports about itself: its codecs and this
// device's playback settings, as the player would send them. Tier and Cause
// pick the files to list for compatTitles.
type compatReq struct {
	Caps       stream.Caps   `json:"caps"`
	Mode       string        `json:"mode"`
	MaxBitrate int           `json:"maxBitrate"`
	AudioLang  string        `json:"audioLang"`
	ForceHLS   bool          `json:"forceHls"`
	Tier       string        `json:"tier"`
	Cause      *stream.Cause `json:"cause"`
}

func (c compatReq) plan(f *store.File, hlsCopy bool) stream.PlanRequest {
	return stream.PlanRequest{FileID: f.ID, Audio: -1, Subtitle: -1, Mode: c.Mode, MaxBitrate: c.MaxBitrate,
		AudioLang: c.AudioLang, ForceHLS: c.ForceHLS, Caps: c.Caps, HLSCopy: hlsCopy}
}

type compatCause struct {
	Tier string `json:"tier"`
	stream.Cause
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

type compatSummary struct {
	Files  int            `json:"files"`
	Remote bool           `json:"remote"`
	Tiers  map[string]int `json:"tiers"`
	Causes []compatCause  `json:"causes"`
}

// assessLibrary runs fn on the verdict for every file the user can see. The
// keyframe index is guessed from the extension (stream.LikelyHLSCopy) so no
// file is opened.
func (s *Server) assessLibrary(r *http.Request, req compatReq, fn func(*store.File, stream.Verdict)) (bool, error) {
	files, err := s.St.FilesForUser(userOf(r).ID)
	if err != nil {
		return false, err
	}
	cfg := s.St.Config()
	remote := s.isRemote(s.clientIP(r))
	for _, f := range files {
		fn(f, stream.Assess(cfg, f, req.plan(f, stream.LikelyHLSCopy(f)), remote))
	}
	return remote, nil
}

// compat summarises how the user's library plays in the requesting browser.
func (s *Server) compat(w http.ResponseWriter, r *http.Request) {
	var req compatReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	sum := compatSummary{Tiers: map[string]int{}, Causes: []compatCause{}}
	byCause := map[compatCause]*compatCause{}
	remote, err := s.assessLibrary(r, req, func(f *store.File, v stream.Verdict) {
		sum.Files++
		sum.Tiers[v.Tier]++
		for _, c := range v.Causes {
			k := compatCause{Tier: v.Tier, Cause: c}
			if byCause[k] == nil {
				byCause[k] = &compatCause{Tier: v.Tier, Cause: c}
			}
			byCause[k].Files++
			byCause[k].Bytes += f.Size
		}
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	sum.Remote = remote
	for _, c := range byCause {
		sum.Causes = append(sum.Causes, *c)
	}
	sort.Slice(sum.Causes, func(i, j int) bool {
		a, b := sum.Causes[i], sum.Causes[j]
		if a.Files != b.Files {
			return a.Files > b.Files
		}
		return a.Tier+a.Kind+a.Key < b.Tier+b.Kind+b.Key
	})
	writeJSON(w, sum)
}

// compatTitles lists the titles in one tier, optionally with one cause.
func (s *Server) compatTitles(w http.ResponseWriter, r *http.Request) {
	var req compatReq
	if err := readJSON(r, &req); err != nil || req.Tier == "" {
		writeErr(w, 400, "bad request")
		return
	}
	var ids []int64
	_, err := s.assessLibrary(r, req, func(f *store.File, v stream.Verdict) {
		if v.Tier != req.Tier {
			return
		}
		if req.Cause == nil {
			ids = append(ids, f.ID)
			return
		}
		for _, c := range v.Causes {
			if c == *req.Cause {
				ids = append(ids, f.ID)
				return
			}
		}
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	list, err := s.St.TitlesForFiles(ids)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, list)
}

// itemCompat says how each of an item's files plays in the requesting
// browser, reading the file's keyframe index as the player would.
func (s *Server) itemCompat(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	var req compatReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "bad request")
		return
	}
	it, err := s.St.ItemForUser(id, userOf(r).ID)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	files, err := s.St.ItemFiles(it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cfg := s.St.Config()
	remote := s.isRemote(s.clientIP(r))
	type fileVerdict struct {
		FileID int64 `json:"fileId"`
		stream.Verdict
	}
	out := []fileVerdict{}
	for _, f := range files {
		hlsCopy := f.Info != nil && s.HLS.CanCopy(f) // only opens Matroska files; cached
		out = append(out, fileVerdict{f.ID, stream.Assess(cfg, f, req.plan(f, hlsCopy), remote)})
	}
	writeJSON(w, map[string]any{"remote": remote, "files": out})
}
