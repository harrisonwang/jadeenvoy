package main

import (
	"fmt"
	"os"

	"github.com/harrisonwang/jadeenvoy/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("je %s (%s)\n", version.Version, version.Commit)
		return
	}
	fmt.Fprintln(os.Stderr, "je CLI is not implemented yet. Use jed HTTP API directly for now.")
	os.Exit(2)
}
