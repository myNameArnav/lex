package api

import (
	"sync"
	"time"

	"lex/internal/store"
)

// sweepGrace keeps derived files younger than this out of a sweep: they may
// belong to a file added (or changed) after the sweep read the database.
const sweepGrace = 10 * time.Minute

var sweepMu sync.Mutex

// FileChanged drops everything derived from a file that was removed from the
// library or replaced: its SSD cache copy, cached subtitles and fonts, seek
// previews and its item's generated thumbnail (regenerated on demand).
// Downloaded subtitles stay with the file's row; Sweep removes them once
// the row is gone.
func (s *Server) FileChanged(f *store.File) {
	if s.Cache != nil {
		s.Cache.Remove(f.ID)
	}
	if s.Subs != nil {
		s.Subs.ForgetFile(f.ID)
	}
	if s.Trick != nil {
		s.Trick.Forget(f.ID)
	}
	if s.Images != nil && f.ItemID > 0 {
		s.Images.ForgetThumb(f.ItemID)
		thumbFailed.Delete(f.ItemID)
		s.Images.ForgetExtraThumb(f.ID)
		thumbFailed.Delete(extraThumbKey(f.ID))
	}
}

// SweepDerived removes derived files whose file or item no longer exists,
// or whose file has changed since they were made: what FileChanged missed
// (files removed while Lex was down, older versions, work that finished
// after the file changed). It only looks inside Lex's own cache folders.
// Concurrent calls are skipped.
func (s *Server) SweepDerived() {
	if !sweepMu.TryLock() {
		return
	}
	defer sweepMu.Unlock()
	// The cutoff is taken before reading the database, so anything created
	// after that read is newer than the cutoff and kept.
	cutoff := time.Now().Add(-sweepGrace)
	files, err := s.St.FileMtimes()
	if err != nil {
		s.Log.Warnf("cleanup: %v", err)
		return
	}
	items, err := s.St.ItemIDs()
	if err != nil {
		s.Log.Warnf("cleanup: %v", err)
		return
	}
	downloads, err := s.St.DownloadedSubPaths()
	if err != nil {
		s.Log.Warnf("cleanup: %v", err)
		return
	}
	n := 0
	if s.Subs != nil {
		n += s.Subs.Sweep(files, downloads, cutoff)
	}
	if s.Trick != nil {
		n += s.Trick.Sweep(files, cutoff)
	}
	if s.Images != nil {
		n += s.Images.SweepThumbs(items, cutoff)
		n += s.Images.SweepExtraThumbs(files, cutoff)
	}
	if n > 0 {
		s.Log.Infof("cleanup: removed %d derived file(s) of removed or replaced media", n)
	}
}
