package meta

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lex/internal/library"
)

var imgExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// firstExisting returns "file:<path>" for the first candidate that exists.
func firstExisting(dir string, names ...string) string {
	if dir == "" {
		return ""
	}
	for _, n := range names {
		for _, ext := range imgExts {
			p := filepath.Join(dir, n+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
				return "file:" + p
			}
		}
	}
	return ""
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// localMovieArt looks for poster/fanart next to a movie.
func localMovieArt(itemPath, filePath string) (poster, backdrop string) {
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	dir := filepath.Dir(filePath)
	if isDir(itemPath) {
		poster = firstExisting(dir, "poster", "folder", "cover", "movie", stem+"-poster", stem)
		backdrop = firstExisting(dir, "fanart", "backdrop", "background", "art", stem+"-fanart", stem+"-backdrop")
	} else {
		poster = firstExisting(dir, stem+"-poster", stem)
		backdrop = firstExisting(dir, stem+"-fanart", stem+"-backdrop")
	}
	return
}

func localShowArt(showPath string) (poster, backdrop string) {
	if !isDir(showPath) {
		return "", ""
	}
	return firstExisting(showPath, "poster", "folder", "cover", "show"), firstExisting(showPath, "fanart", "backdrop", "background", "art")
}

func localSeasonArt(showPath string, season int) string {
	if !isDir(showPath) {
		return ""
	}
	names := []string{fmt.Sprintf("season%02d-poster", season), fmt.Sprintf("season%d-poster", season), fmt.Sprintf("Season%02d", season)}
	if season == 0 {
		names = append(names, "season-specials-poster")
	}
	if p := firstExisting(showPath, names...); p != "" {
		return p
	}
	ents, err := os.ReadDir(showPath)
	if err != nil {
		return ""
	}
	for _, e := range ents {
		if e.IsDir() && library.SeasonFromDir(e.Name()) == season {
			if p := firstExisting(filepath.Join(showPath, e.Name()), "poster", "folder", "cover"); p != "" {
				return p
			}
		}
	}
	return ""
}

func localEpisodeThumb(filePath string) string {
	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return firstExisting(filepath.Dir(filePath), stem+"-thumb", stem)
}
