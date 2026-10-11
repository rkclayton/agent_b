package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
)

func isInaccessible(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}

func withSkippedInaccessible(result string, skipped int) string {
	if skipped == 0 {
		return result
	}
	if strings.TrimSpace(result) != "" {
		result += "\n"
	}
	return result + fmt.Sprintf("[skipped %d inaccessible]", skipped)
}
