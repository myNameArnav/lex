package api

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lex/internal/meta"
	"lex/internal/store"
)

func TestCastImages(t *testing.T) {
	s, handler := securityServer(t)
	s.Images = meta.NewImageCache(t.TempDir())
	s.resizeSem = make(chan struct{}, 2)
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	library, err := s.St.CreateLibrary("Movies", "movies", []string{"/srv/media"})
	if err != nil {
		t.Fatal(err)
	}
	portraitURL := "https://images.example.org/portrait.png"
	// Seed the existing download cache; the endpoint must serve this through the
	// same resize/cache pipeline as posters without reaching an external service.
	var portrait bytes.Buffer
	if err := png.Encode(&portrait, image.NewRGBA(image.Rect(0, 0, 480, 720))); err != nil {
		t.Fatal(err)
	}
	cacheName := fmt.Sprintf("%x", sha1.Sum([]byte(portraitURL)))[:20] + ".png"
	if err := os.WriteFile(filepath.Join(s.Images.Dir, cacheName), portrait.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	privateHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("cast image reached a private address")
	}))
	defer privateHost.Close()
	item := &store.Item{LibraryID: library.ID, Kind: "movie", Title: "Sample film", Cast: []store.Person{
		{Name: "Director", Role: "Director"},
		{Name: "Performer", Image: portraitURL},
		{Name: "Private host", Image: privateHost.URL + "/portrait.png"},
		{Name: "Local file", Image: "file:///etc/passwd"},
	}}
	if _, err := s.St.InsertItem(item); err != nil {
		t.Fatal(err)
	}
	if err := s.St.SaveMetadata(item); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/items/%d/cast/", item.ID)
	for _, tc := range []struct {
		name, path string
		auth       bool
		want       int
	}{
		{"anonymous", base + "1/image", false, 401},
		{"viewer portrait", base + "1/image?w=160", true, 200},
		{"URL override ignored", base + "1/image?url=file:///etc/passwd", true, 200},
		{"missing portrait", base + "0/image", true, 404},
		{"out of range", base + "4/image", true, 404},
		{"negative index", base + "-1/image", true, 400},
		{"invalid index", base + "bad/image", true, 400},
		{"invalid item", "/api/items/bad/cast/1/image", true, 400},
		{"missing item", "/api/items/99999/cast/1/image", true, 404},
		{"private address", base + "2/image", true, 404},
		{"local file", base + "3/image", true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://lex.test"+tc.path, nil)
			if tc.auth {
				r.AddCookie(&http.Cookie{Name: "lex_token", Value: token})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if tc.want == 200 {
				if !strings.HasPrefix(w.Header().Get("Cache-Control"), "private,") {
					t.Fatal("portrait response must remain private")
				}
				cfg, _, err := image.DecodeConfig(w.Body)
				if err != nil {
					t.Fatal(err)
				}
				wantWidth := 480
				if strings.Contains(tc.path, "w=160") {
					wantWidth = 160
				}
				if cfg.Width != wantWidth || cfg.Height != wantWidth*3/2 {
					t.Fatalf("portrait size = %dx%d", cfg.Width, cfg.Height)
				}
			}
		})
	}
}

func TestMetadataPosterProxy(t *testing.T) {
	s, handler := securityServer(t)
	s.Images = meta.NewImageCache(t.TempDir())
	s.resizeSem = make(chan struct{}, 2)
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := s.St.CreateUser("viewer", "test-password-123", false)
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	viewerToken, _ := s.St.CreateToken(viewer.ID, "Test", "127.0.0.1")
	posterURL := "https://image.tmdb.org/t/p/w185/poster.png"
	var poster bytes.Buffer
	if err := png.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 185, 278))); err != nil {
		t.Fatal(err)
	}
	cacheName := fmt.Sprintf("%x", sha1.Sum([]byte(posterURL)))[:20] + ".png"
	if err := os.WriteFile(filepath.Join(s.Images.Dir, cacheName), poster.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if got := metaPosterURL(posterURL); got != "/api/admin/metadata/poster?url=https%3A%2F%2Fimage.tmdb.org%2Ft%2Fp%2Fw185%2Fposter.png" {
		t.Fatalf("metaPosterURL = %q", got)
	}
	if metaPosterURL("") != "" {
		t.Fatal("an empty poster must stay empty")
	}
	for _, tc := range []struct {
		name, url, token string
		want             int
	}{
		{"anonymous", posterURL, "", 401},
		{"viewer", posterURL, viewerToken, 403},
		{"admin", posterURL, adminToken, 200},
		{"host not allowed", "https://example.org/poster.png", adminToken, 400},
		{"lookalike host", "https://image.tmdb.org.example.org/poster.png", adminToken, 400},
		{"explicit port", "https://image.tmdb.org:8443/poster.png", adminToken, 400},
		{"credentials", "https://user:pw@image.tmdb.org/poster.png", adminToken, 400},
		{"file scheme", "file:///etc/passwd", adminToken, 400},
		{"missing url", "", adminToken, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://lex.test"+"/api/admin/metadata/poster?url="+url.QueryEscape(tc.url), nil)
			if tc.token != "" {
				r.AddCookie(&http.Cookie{Name: "lex_token", Value: tc.token})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if tc.want == 200 {
				if !strings.HasPrefix(w.Header().Get("Cache-Control"), "private,") {
					t.Fatal("poster response must be private")
				}
				if cfg, _, err := image.DecodeConfig(w.Body); err != nil || cfg.Width != 185 {
					t.Fatalf("poster = %+v, %v", cfg, err)
				}
			}
		})
	}
}
