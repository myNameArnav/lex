package arr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeArr is a minimal Servarr notification API. Its Test only succeeds for
// webhook URLs starting with one of reach, like a containerised Radarr that
// can reach the LAN but not a Tailscale name.
type fakeArr struct {
	mu    sync.Mutex
	list  []map[string]any
	tests []string
	reach []string
}

const schema = `[{"implementation":"Slack","fields":[{"name":"webHook"}]},
{"implementation":"Webhook","implementationName":"Webhook","name":"","fields":[{"name":"url","value":""},{"name":"method","value":1},{"name":"username"}],
"onGrab":false,"supportsOnGrab":true,"onDownload":false,"supportsOnDownload":true,"onUpgrade":false,"supportsOnUpgrade":true,
"onRename":false,"supportsOnRename":false,"onMovieFileDelete":false,"supportsOnMovieFileDelete":true,
"onHealthIssue":false,"supportsOnHealthIssue":true,"onImportComplete":false,"supportsOnImportComplete":true,"futureSetting":"kept"}]`

func (f *fakeArr) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Api-Key") != "key" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&body)
	}
	switch p := strings.TrimPrefix(r.URL.Path, "/base/api/v3"); {
	case p == "/system/status":
		w.Write([]byte(`{"appName":"Radarr","version":"5.14.0"}`))
	case p == "/notification/schema":
		w.Write([]byte(schema))
	case p == "/notification" && r.Method == "GET":
		json.NewEncoder(w).Encode(f.list)
	case p == "/notification/test":
		u := notification(body).url()
		f.tests = append(f.tests, u)
		for _, ok := range f.reach {
			if strings.HasPrefix(u, ok) {
				return
			}
		}
		w.WriteHeader(400)
		w.Write([]byte(`[{"propertyName":"Url","errorMessage":"Unable to send test message: Connection refused"}]`))
	case p == "/notification" && r.Method == "POST":
		body["id"] = float64(len(f.list) + 1)
		f.list = append(f.list, body)
		w.WriteHeader(201)
	case strings.HasPrefix(p, "/notification/") && r.Method == "PUT":
		for i, n := range f.list {
			if n["id"] == body["id"] {
				f.list[i] = body
			}
		}
		w.WriteHeader(202)
	default:
		w.WriteHeader(404)
	}
}

func TestConnectPicksTheFirstReachableAddress(t *testing.T) {
	f := &fakeArr{reach: []string{"http://192.168.0.49:8420"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Base: srv.URL + "/base", Key: "key"}
	path := ScanPath + "?token=abc"
	bases := []string{"https://lex.example.org", "http://192.168.0.49:8420", "http://10.89.0.1:8420"}

	st, err := c.Connect(context.Background(), bases, path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Connected || st.URL != "http://192.168.0.49:8420"+ScanPath || st.App != "Radarr" {
		t.Fatalf("status = %+v", st)
	}
	if len(f.tests) != 2 {
		t.Fatalf("tested %v, want the Tailscale name then the LAN address", f.tests)
	}
	if len(f.list) != 1 {
		t.Fatalf("%d connections, want 1", len(f.list))
	}
	n := notification(f.list[0])
	if n["name"] != Name || n.url() != "http://192.168.0.49:8420"+path || n["futureSetting"] != "kept" {
		t.Fatalf("saved %v", n)
	}
	for k, want := range map[string]bool{"onDownload": true, "onUpgrade": true, "onMovieFileDelete": true, "onImportComplete": true,
		"onGrab": false, "onHealthIssue": false, "onRename": false /* unsupported */} {
		if n[k] != want {
			t.Errorf("%s = %v, want %v", k, n[k], want)
		}
	}

	// Connecting again (say, after the token changed) updates the same
	// connection instead of adding a second one.
	f.reach = []string{"https://lex.example.org"}
	if _, err := c.Connect(context.Background(), bases, ScanPath+"?token=new"); err != nil {
		t.Fatal(err)
	}
	if len(f.list) != 1 || notification(f.list[0]).url() != "https://lex.example.org"+ScanPath+"?token=new" {
		t.Fatalf("after reconnect: %v", f.list)
	}

	st, err = c.Check(context.Background(), ScanPath+"?token=new")
	if err != nil || !st.Connected {
		t.Fatalf("Check = %+v, %v", st, err)
	}
	if st, _ = c.Check(context.Background(), ScanPath+"?token=rotated"); st.Connected {
		t.Fatal("a webhook with an old token counted as connected")
	}
}

func TestConnectReportsWhyNothingWorked(t *testing.T) {
	f := &fakeArr{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Base: srv.URL + "/base", Key: "key"}
	_, err := c.Connect(context.Background(), []string{"http://192.168.0.49:8420"}, ScanPath+"?token=abc")
	if err == nil || !strings.Contains(err.Error(), "Connection refused") || len(f.list) != 0 {
		t.Fatalf("err = %v, saved %d", err, len(f.list))
	}

	c.Key = "wrong"
	if _, err := c.Connect(context.Background(), []string{"http://x"}, ScanPath); !errors.Is(err, ErrBadKey) {
		t.Fatalf("bad key: %v", err)
	}
	c.Base = "http://127.0.0.1:1"
	if _, err := c.Check(context.Background(), ScanPath); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("unreachable: %v", err)
	}
}
