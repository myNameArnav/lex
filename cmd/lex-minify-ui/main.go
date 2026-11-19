// Command lex-minify-ui minifies the browser UI in place, with source maps:
//
//	go run ./cmd/lex-minify-ui web/static
//
// The Dockerfile runs it before go build. It rewrites the files it is given,
// so run it on a build copy (as the image build does), not a working
// checkout.
package main

import (
	"fmt"
	"os"

	"lex/internal/uibuild"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: lex-minify-ui <web/static dir>")
		os.Exit(2)
	}
	files, err := uibuild.Minify(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "lex-minify-ui:", err)
		os.Exit(1)
	}
	var before, after int
	for _, f := range files {
		before += f.Before
		after += f.After
	}
	fmt.Printf("lex-minify-ui: %d files, %d KB -> %d KB\n", len(files), before/1024, after/1024)
}
