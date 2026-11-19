// Command lex-apitypes writes web/types/api.gen.d.ts: the JSON API's routes
// and types for the UI's TypeScript check, generated from internal/api.
// Run it from the repository root after changing the API.
package main

import (
	"flag"
	"log"
	"os"

	"lex/internal/apitypes"
)

func main() {
	out := flag.String("o", "web/types/api.gen.d.ts", "output file")
	flag.Parse()
	b, err := apitypes.Generate(".", "./internal/api")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		log.Fatal(err)
	}
}
