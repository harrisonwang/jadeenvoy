package main

import (
	"fmt"
	"os"

	"github.com/harrisonwang/jadeenvoy/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("je-vault %s (%s)\n", version.Version, version.Commit)
		return
	}
	fmt.Fprintln(os.Stderr, "je-vault MITM proxy is not implemented yet.")
	os.Exit(2)
}
