// Command notes is the applenotes CLI. For now it decodes a raw ZICNOTEDATA
// blob on stdin, which is what the read path is validated against.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/gig3m/applenotes/internal/notestore"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "decode" {
		fmt.Fprintln(os.Stderr, "usage: notes decode < blob.gz")
		os.Exit(2)
	}
	blob, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	n, err := notestore.Decode(blob)
	if err != nil {
		fatal(err)
	}
	fmt.Print(n.Markdown())
	fmt.Println()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "notes:", err)
	os.Exit(1)
}
