//go:build !windows

package tools

import "os"

func AtomicReplace(source, target string) error { return os.Rename(source, target) }
