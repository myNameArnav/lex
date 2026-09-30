package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"lex/internal/proc"
	"lex/internal/store"
)

var thumbFailed sync.Map // item id -> time of failure

// imageRef picks the artwork reference for an item with sensible fallbacks.
func (s *Server) imageRef(it *store.Item, kind string) string {
	parent := func(id int64) *store.Item {
		if id == 0 {
			return nil
		}
		p, err := s.St.Item(id)
		if err != nil {
			return nil
		}
		return p
	}
	switch kind {
	case "poster":
		if it.Poster != "" {
			return it.Poster
		}
		if it.Kind == "episode" {
			if p := parent(it.ParentID); p != nil && p.Poster != "" {
				return p.Poster
			}
		}
		if p := parent(it.ShowID); p != nil {
			return p.Poster
		}
	case "backdrop":
		if it.Backdrop != "" {
			return it.Backdrop
		}
		if p := parent(it.ShowID); p != nil && p.Backdrop != "" {
			return p.Backdrop
		}
	case "thumb":
		if it.Thumb != "" {
			return it.Thumb
		}
		if it.Kind == "movie" && it.Backdrop != "" {
			return it.Backdrop
		}
		if s.St.Config().GenerateThumbs && (it.Kind == "episode" || it.Kind == "movie") {
			return "gen:"
		}
		if p := parent(it.ShowID); p != nil && p.Backdrop != "" {
			return p.Backdrop
		}
	}
	return ""
}

func (s *Server) resolveImage(ctx context.Context, it *store.Item, ref string) (string, error) {
	switch {
	case ref == "":
		return "", fmt.Errorf("no image")
	case ref == "gen:":
		return s.generateThumb(ctx, it)
	case strings.HasPrefix(ref, "url:"):
		c, err := s.Images.Fetch(ctx, strings.TrimPrefix(ref, "url:"))
		if err != nil {
			return "", err
		}
		return s.Images.Path(c), nil
	default:
		p := s.Images.Path(ref)
		if p == "" {
			return "", fmt.Errorf("bad image ref")
		}
		return p, nil
	}
}

func (s *Server) itemImage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, "bad id")
		return
	}
	kind := r.PathValue("kind")
	if kind != "poster" && kind != "backdrop" && kind != "thumb" {
		writeErr(w, 400, "bad kind")
		return
	}
	it, err := s.St.Item(id)
	if err != nil {
		notFoundOr500(w, err)
		return
	}
	ref := s.imageRef(it, kind)
	p, err := s.resolveImage(r.Context(), it, ref)
	if err != nil || p == "" {
		// Episode thumb generation failed: fall back to show backdrop.
		if kind == "thumb" && ref == "gen:" {
			if show, e := s.St.Item(it.ShowID); e == nil && show.Backdrop != "" {
				p, err = s.resolveImage(r.Context(), show, show.Backdrop)
			}
		}
		if err != nil || p == "" {
			w.Header().Set("Cache-Control", "private, max-age=300")
			http.Error(w, "no image", 404)
			return
		}
	}
	if wd := snapWidth(qInt(r, "w", 0)); wd > 0 {
		if rp, err := s.resized(p, wd); err == nil {
			p = rp
		} else {
			s.Log.Debugf("resize %s: %v", p, err)
		}
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "no image", 404)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Cache-Control", "private, max-age=2592000")
	http.ServeContent(w, r, filepath.Base(p), st.ModTime(), f)
}

func snapWidth(w int) int {
	if w <= 0 {
		return 0
	}
	for _, s := range []int{160, 240, 320, 480, 640, 960, 1280} {
		if w <= s {
			return s
		}
	}
	return 0 // bigger than our largest bucket: serve original
}

// resized returns a cached JPEG of src scaled down to width w.
func (s *Server) resized(src string, w int) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	h := sha1.Sum([]byte(src + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10)))
	dst := filepath.Join(s.Images.Dir, "r", fmt.Sprintf("%d-%s.jpg", w, hex.EncodeToString(h[:10])))
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	s.resizeSem <- struct{}{}
	defer func() { <-s.resizeSem }()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		f.Close()
		return "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 20_000_000 {
		f.Close()
		return "", fmt.Errorf("image dimensions exceed resize limit")
	}
	if cfg.Width <= w {
		f.Close()
		return src, nil
	}
	f.Seek(0, 0)
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", err
	}
	b := img.Bounds()
	nh := max(1, b.Dy()*w/b.Dx())
	out := image.NewRGBA(image.Rect(0, 0, w, nh))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), img, b, draw.Src, nil)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	of, err := os.CreateTemp(filepath.Dir(dst), ".resize-*.jpg")
	if err != nil {
		return "", err
	}
	tmp := of.Name()
	defer os.Remove(tmp)
	if err := jpeg.Encode(of, out, &jpeg.Options{Quality: 82}); err != nil {
		of.Close()
		os.Remove(tmp)
		return "", err
	}
	of.Close()
	return dst, os.Rename(tmp, dst)
}

// generateThumb grabs a frame from the video for items without artwork.
func (s *Server) generateThumb(ctx context.Context, it *store.Item) (string, error) {
	dst := s.Images.ThumbPath(it.ID)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if t, ok := thumbFailed.Load(it.ID); ok && time.Since(t.(time.Time)) < time.Hour {
		return "", fmt.Errorf("thumbnail generation failed recently")
	}
	files, err := s.St.ItemFiles(it.ID)
	if err != nil || len(files) == 0 {
		return "", fmt.Errorf("no file")
	}
	f := files[0]
	select {
	case s.thumbSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.thumbSem }()
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	at := 60.0
	if f.Duration > 0 {
		at = f.Duration * 0.2
	}
	tctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	tmp := dst + ".tmp.jpg"
	cmd := exec.CommandContext(tctx, s.FF.Path, "-hide_banner", "-nostdin", "-loglevel", "error", "-ss", strconv.FormatFloat(at, 'f', 1, 64),
		"-skip_frame", "nokey", "-i", f.Path, "-frames:v", "1", "-vf", "scale=640:-2,format=yuvj420p", "-q:v", "5", "-y", tmp)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	proc.Nice(cmd.Process.Pid, 10)
	if err := cmd.Wait(); err != nil {
		os.Remove(tmp)
		thumbFailed.Store(it.ID, time.Now())
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}
