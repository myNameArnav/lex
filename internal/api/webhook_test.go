package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lex/internal/library"
	"lex/internal/meta"
)

// radarrRequest mimics Radarr/Sonarr's Connect → Webhook "Test": a JSON body,
// no Origin or Sec-Fetch-* headers, and POST or PUT (the method is a setting).
func radarrRequest(method, url string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(`{"eventType":"Test","instanceName":"Radarr"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "Radarr/7.0.3.10918 (ubuntu 24.04)")
	r.Header.Set("Accept-Encoding", "gzip, br")
	return r
}

func TestWebhookScanAcceptsRadarrRequests(t *testing.T) {
	s, h := securityServer(t)
	s.Scanner = library.NewScanner(s.St, "", s.Log)
	// Let the scans the webhook starts finish before the store closes.
	t.Cleanup(func() {
		for s.Scanner.Status().Running {
			time.Sleep(5 * time.Millisecond)
		}
	})
	token := s.St.Config().WebhookToken
	for _, method := range []string{"POST", "PUT"} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, radarrRequest(method, "http://192.168.0.49:8420/api/webhook/scan?token="+token))
			if w.Code != 200 {
				t.Fatalf("%s: status %d, body %q", method, w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			h.ServeHTTP(w, radarrRequest(method, "http://192.168.0.49:8420/api/webhook/scan?token=wrong"))
			if w.Code != 403 {
				t.Fatalf("%s with a bad token: status %d", method, w.Code)
			}
		})
	}
}

func TestAdminInfoOffersLANWebhookURLs(t *testing.T) {
	s, h := securityServer(t)
	s.ListenAddr = ":8420"
	s.Images = &meta.ImageCache{Dir: t.TempDir()}
	s.DataDir = t.TempDir()
	admin, err := s.St.CreateInitialAdmin("admin", "test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := s.St.CreateToken(admin.ID, "Test", "127.0.0.1")
	w := adminRequest(t, h, token, "GET", "/api/admin/info", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var info struct {
		WebhookURL     string   `json:"webhookUrl"`
		WebhookLANURLs []string `json:"webhookLanUrls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.WebhookLANURLs == nil {
		t.Fatal("webhookLanUrls missing")
	}
	for _, u := range info.WebhookLANURLs {
		if !strings.HasPrefix(u, "http://") || !strings.HasSuffix(u, ":8420") {
			t.Errorf("unexpected LAN URL %q", u)
		}
	}
}

func TestLANWebhookBases(t *testing.T) {
	addrs := []string{"127.0.0.1/8", "192.168.0.49/24", "10.89.0.1/24", "100.127.170.32/32", "fd7a:115c:a1e0::1/128", "8.8.8.8/32"}
	got := lanBases(":8420", addrs)
	want := []string{"http://192.168.0.49:8420", "http://10.89.0.1:8420"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lanBases = %v, want %v", got, want)
	}
	if got := lanBases("127.0.0.1:8420", addrs); len(got) != 0 {
		t.Fatalf("loopback listener should offer no LAN URLs, got %v", got)
	}
	if got := lanBases("192.168.0.49:9000", addrs); strings.Join(got, ",") != "http://192.168.0.49:9000" {
		t.Fatalf("bound listener: got %v", got)
	}
}

func TestLexBasesOrder(t *testing.T) {
	addrs := []string{"127.0.0.1/8", "192.168.0.49/24", "100.64.0.5/10", "10.89.0.1/24"}
	got := lexBases("https://lex.example.org", ":8420", addrs)
	want := []string{"https://lex.example.org", "http://host.containers.internal:8420", "http://host.docker.internal:8420", "http://192.168.0.49:8420", "http://10.89.0.1:8420", "http://127.0.0.1:8420"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("lexBases = %v, want %v", got, want)
	}
	// The browser already on a LAN address isn't offered twice; junk origins are ignored.
	got = lexBases("http://192.168.0.49:8420", "192.168.0.49:8420", addrs)
	if strings.Join(got, " ") != "http://192.168.0.49:8420" {
		t.Fatalf("lexBases = %v", got)
	}
	if got = lexBases("javascript:alert(1)", "127.0.0.1:8420", nil); strings.Join(got, " ") != "http://127.0.0.1:8420" {
		t.Fatalf("lexBases = %v", got)
	}
}
