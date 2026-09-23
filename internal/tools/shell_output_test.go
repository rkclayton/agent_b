package tools

import (
	"strings"
	"testing"
)

func TestCleanConsoleOutputStripsANSIAndRepairsEncoding(t *testing.T) {
	raw := "\x1b[31;1mred\x1b[0m\n\x1b]0;title\x07ok" + string([]byte{0xff})
	clean := cleanConsoleOutput(raw)
	if clean != "red\nok�" || strings.Contains(clean, "\x1b") {
		t.Fatalf("clean=%q", clean)
	}
}
