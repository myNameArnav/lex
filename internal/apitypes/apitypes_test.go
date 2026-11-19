package apitypes

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The checked-in declarations must match the API, or the UI's type check
// runs against stale types.
func TestGeneratedFileUpToDate(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	want, err := Generate(root, "./internal/api")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "web/types/api.gen.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("web/types/api.gen.d.ts is out of date with internal/api: run go run ./cmd/lex-apitypes")
	}
}

func TestGenerate(t *testing.T) {
	b, err := Generate(".", "./testdata/srv")
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		// Structs as encoding/json encodes them: tag names, omitempty as
		// optional, "-" and unexported fields left out, embedded fields
		// promoted, time.Time and []byte as strings, ,string as a string.
		"// srv.Item\nexport interface Item {\n  title: string;\n  year?: number;\n  parent: Item | null;\n  raw: string;\n  tags: Record<string, string>;\n  count: string;\n  NoTag: boolean;\n  id: number;\n  created: string;\n}\n",
		"export interface Other {\n  item: Item;\n}\n",
		// A handler's map-literal shapes merge: keys not in every shape (or
		// assigned later) are optional.
		"[\"GET\", `/api/items/${string}`, { item: Item | null; short?: boolean; other?: Other; }, any]",
		// readJSON's type is the body; a helper given the ResponseWriter
		// writes the response.
		"[\"PUT\", `/api/items/${string}`, Item[], itemReq]",
		"export interface itemReq {\n  title: string;\n}\n",
		// A map with keys only known at run time stays a map.
		"[\"GET\", `/api/items/${string}/children`, Record<string, any>, any]",
		// An inline handler; a handler that writes no JSON is untyped.
		"[\"GET\", `/api/info`, { name: string; uptime: number; }, any]",
		"[\"GET\", `/api/image`, any, any]",
		"[\"GET\", `/api/${string}`, any, any]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n%s\n\nin:\n%s", want, out)
		}
	}
	// The catch-all subtree isn't a route.
	if strings.Contains(out, "`/api/`") {
		t.Errorf("catch-all /api/ listed as a route:\n%s", out)
	}
	// More specific paths come first: ${string} also matches slashes.
	if strings.Index(out, "/api/items/${string}/children") > strings.Index(out, "`/api/items/${string}`") {
		t.Errorf("/api/items/{id}/children must come before /api/items/{id}:\n%s", out)
	}
}
