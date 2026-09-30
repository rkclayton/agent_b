// Package chatstore maps durable chats to the real directory tree operators see.
package chatstore

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
)

const maxComponentUnits = 255

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeName returns one valid Windows directory component. Display labels
// remain unchanged; only their on-disk representation passes through here.
func SanitizeName(label string) string {
	var value strings.Builder
	for _, r := range label {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			r = '_'
		}
		value.WriteRune(r)
	}
	name := strings.TrimRight(value.String(), ". ")
	if name == "" || name == "." || name == ".." {
		name = "chat"
	}
	base := name
	if at := strings.IndexByte(base, '.'); at >= 0 {
		base = base[:at]
	}
	if reservedNames[strings.ToUpper(base)] {
		name = "_" + name
	}
	name = truncateUnits(name, maxComponentUnits)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return "chat"
	}
	return name
}

// AvailableName applies deterministic, case-insensitive counters to collisions.
func AvailableName(root, label string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	used := make(map[string]bool, len(entries))
	for _, entry := range entries {
		used[strings.ToLower(entry.Name())] = true
	}
	base := SanitizeName(label)
	if !used[strings.ToLower(base)] {
		return base, nil
	}
	for counter := 2; ; counter++ {
		suffix := fmt.Sprintf(" (%d)", counter)
		candidate := strings.TrimRight(truncateUnits(base, maxComponentUnits-utf16Units(suffix)), ". ") + suffix
		if !used[strings.ToLower(candidate)] {
			return candidate, nil
		}
	}
}

func utf16Units(value string) int { return len(utf16.Encode([]rune(value))) }

func truncateUnits(value string, maximum int) string {
	var result strings.Builder
	units := 0
	for _, r := range value {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > maximum {
			break
		}
		result.WriteRune(r)
		units += size
	}
	return result.String()
}
