package agent

import "testing"

// Item 2go (v1.2.5): the name comes from the operator's first message, once.
// These are the cases the rule is written in terms of: the first clause or the
// first six words, whichever is shorter, trimmed of punctuation.
func TestFirstMessageNameTakesTheShorterOfClauseAndSixWords(t *testing.T) {
	for _, item := range []struct{ message, want string }{
		{"fix the login bug in vesper", "fix the login bug in vesper"},
		{"fix the login bug in vesper and then deploy it", "fix the login bug in vesper"},
		{"Fix the login bug. Then deploy.", "Fix the login bug"},
		{"read AGENTS.md, then tell me what it says", "read AGENTS.md"},
		{"why is the gate red?", "why is the gate red"},
		{"deploy", "deploy"},
		{"  padded   spacing   here  ", "padded spacing here"},
		{"first line\nsecond line", "first line"},
		{"a, b", "a"},
		{"!!!", ""},
		{"", ""},
		// A dotted version or a file name is not the end of a thought.
		{"bump to v1.2.5 everywhere", "bump to v1.2.5 everywhere"},
		{"edit user.go so it compiles", "edit user.go so it compiles"},
	} {
		if got := firstMessageName(item.message); got != item.want {
			t.Fatalf("firstMessageName(%q) = %q, want %q", item.message, got, item.want)
		}
	}
}

func TestFirstMessageNameIsBounded(t *testing.T) {
	long := ""
	for i := 0; i < 40; i++ {
		long += "abcdefghij "
	}
	name := firstMessageName(long)
	if len([]rune(name)) > 80 {
		t.Fatalf("name is %d runes: %q", len([]rune(name)), name)
	}
	if words := len([]rune(name)); words == 0 {
		t.Fatal("a long message still has a name")
	}
}
