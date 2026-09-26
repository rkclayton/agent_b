//go:build windows

package tools

import (
	"os"
	"strings"
)

// interpreterExtensions is PATHEXT, so a candidate is found the way Windows would
// find it -- and the extensionless name is tried LAST, because an extensionless
// entry on this host was a zero-byte file, not a program.
func interpreterExtensions() []string {
	extensions := make([]string, 0, 8)
	for _, extension := range strings.Split(os.Getenv("PATHEXT"), ";") {
		if extension = strings.TrimSpace(extension); extension != "" {
			extensions = append(extensions, strings.ToLower(extension))
		}
	}
	if len(extensions) == 0 {
		extensions = []string{".com", ".exe", ".bat", ".cmd"}
	}
	return append(extensions, "")
}
