package attachment

import "testing"

func TestSanitizeAttachmentBasenameStripsPathsControlsAndLeadingDots(t *testing.T) {
	got, err := SanitizeName("..\\folder/..\x00report.txt")
	if err != nil || got != "report.txt" {
		t.Fatalf("SanitizeName=%q, %v", got, err)
	}
}

func TestSanitizeAttachmentBasenameRefusesEmptyAndWindowsDeviceNames(t *testing.T) {
	for _, name := range []string{"...", "NUL", "con.txt", `folder\\LPT9.log`} {
		if got, err := SanitizeName(name); err == nil {
			t.Fatalf("SanitizeName(%q)=%q, want error", name, got)
		}
	}
}
