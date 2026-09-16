package main

import (
	"flag"
	"fmt"
	"os"

	"harness/internal/projectorpins"
)

func main() {
	record := flag.Bool("record", false, "replace the non-blocking wide-sweep baseline")
	flag.Parse()
	root, err := projectorpins.RepoRoot(".")
	if err == nil {
		_, err = projectorpins.Sweep(root, *record)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
