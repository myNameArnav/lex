package api

import (
	"net/http"

	"lex/internal/store"
)

// Per-user library access. Admins choose, per regular user, every library
// or some; admins see all. Whatever lives in a library the user can't see
// (items, files, images, subtitles, Live TV programs) answers 404 exactly
// like an id that doesn't exist, so ids don't reveal what's there.
//
// Item queries made for a user filter in SQL (store.libAccess); handlers
// that load an item or file by id check it here.

// itemFor loads item id for the signed-in user, answering 404 if it doesn't
// exist or is in a library they can't see.
func (s *Server) itemFor(w http.ResponseWriter, r *http.Request, id int64) (*store.Item, bool) {
	it, err := s.St.Item(id)
	if err == nil && !userOf(r).CanAccess(it.LibraryID) {
		err = store.ErrNotFound
	}
	if err != nil {
		notFoundOr500(w, err)
		return nil, false
	}
	return it, true
}

// pathItem is itemFor for the {id} path value.
func (s *Server) pathItem(w http.ResponseWriter, r *http.Request) (*store.Item, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	return s.itemFor(w, r, id)
}

// fileFor loads file id for the signed-in user, like itemFor.
func (s *Server) fileFor(w http.ResponseWriter, r *http.Request, id int64) (*store.File, bool) {
	f, err := s.St.File(id)
	if err == nil && !userOf(r).CanAccess(f.LibraryID) {
		err = store.ErrNotFound
	}
	if err != nil {
		notFoundOr500(w, err)
		return nil, false
	}
	return f, true
}

// pathFile is fileFor for the {id} path value.
func (s *Server) pathFile(w http.ResponseWriter, r *http.Request) (*store.File, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return nil, false
	}
	return s.fileFor(w, r, id)
}

// visibleLibraries is the libraries u may see.
func visibleLibraries(libs []store.Library, u *store.User) []store.Library {
	var out []store.Library
	for _, l := range libs {
		if u.CanAccess(l.ID) {
			out = append(out, l)
		}
	}
	return out
}
