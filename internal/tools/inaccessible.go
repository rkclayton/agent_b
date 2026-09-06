package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

func isInaccessible(err error) bool {
	return errors.Is(err, fs.ErrPermission)
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
