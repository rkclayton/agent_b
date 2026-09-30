package chatstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSanitizeNameWindowsTable(t *testing.T) {
	tests := []struct{ input, want string }{
		{"CON", "_CON"}, {"con.txt", "_con.txt"}, {"PRN", "_PRN"}, {"AUX.log", "_AUX.log"}, {"NUL", "_NUL"},
		{"COM1", "_COM1"}, {"com9.txt", "_com9.txt"}, {"LPT1", "_LPT1"}, {"lpt9.md", "_lpt9.md"},
		{`bad<>:"/\|?*name`, "bad_________name"}, {"control\x00\x1fname", "control__name"},
		{"", "chat"}, {".", "chat"}, {"..", "chat"}, {"trailing. .  ", "trailing"},
		{strings.Repeat("a", 200), strings.Repeat("a", 200)}, {"会議 🚀", "会議 🚀"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			if got := SanitizeName(test.input); got != test.want {
				t.Fatalf("SanitizeName(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
	long := SanitizeName(strings.Repeat("🚀", 200))
	if units := len(utf16.Encode([]rune(long))); units > 255 {
		t.Fatalf("long name has %d UTF-16 units: %q", units, long)
	}
}

func TestAvailableNameUsesCaseInsensitiveDeterministicCounters(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Same", "same (2)"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := AvailableName(root, "same"); err != nil || got != "same (3)" {
		t.Fatalf("AvailableName same = %q, %v", got, err)
	}
	if got, err := AvailableName(root, "CON"); err != nil || got != "_CON" {
		t.Fatalf("AvailableName CON = %q, %v", got, err)
	}
	long, err := AvailableName(root, strings.Repeat("x", 255))
	if err != nil {
		t.Fatal(err)
	}
	if units := len(utf16.Encode([]rune(long))); units > 255 {
		t.Fatalf("counter candidate has %d UTF-16 units", units)
	}
}
