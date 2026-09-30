package meta

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	got := redact("https://user:password@example.org/image?api_key=secret&token=secret#secret")
	if got != "https://example.org/image" {
		t.Fatalf("redact=%s", got)
	}
	if got := redact("http://%zz?token=secret"); got != "[invalid URL]" {
		t.Fatalf("invalid URL=%s", got)
	}
}

func TestArtworkRejectsLocalAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1", "169.254.169.254", "::1", "fe80::1", "fd00::1", "::", "0.0.0.0", "224.0.0.1", "::ffff:127.0.0.1"} {
		if publicArtworkIP(net.ParseIP(address)) {
			t.Errorf("accepted %s", address)
		}
	}
	if !publicArtworkIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address rejected")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("artwork reached loopback") }))
	defer server.Close()
	cache := NewImageCache(t.TempDir())
	if _, err := cache.Fetch(context.Background(), server.URL+"/image?token=test-secret"); err == nil || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, u := range []string{"file:///etc/passwd", "http://user:password@example.org/image"} {
		if _, err := cache.Fetch(context.Background(), u); err == nil {
			t.Fatalf("accepted %s", u)
		}
	}
}
