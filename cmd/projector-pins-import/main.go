package main

import (
	"fmt"
	"os"

	"harness/internal/projectorpins"
)

func main() {
	root, err := projectorpins.RepoRoot(".")
	if err == nil {
		err = projectorpins.ImportSources(root)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
