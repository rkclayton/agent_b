package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"harness/internal/session"
)

// outsideDecision is what the routing guard does with a command or script
// (item 2fz): card names existing outside paths it would read (the operator's
// decision, as 2fi); missing names outside paths it would read that do not
// exist (a plain error, no card). Both empty: the command runs as the shell
// always has, under the OS boundary — the shell stays unjailed.
type outsideDecision struct {
	card, missing string
}

// statementBreak splits a command line into statements: && and || before the
// single separators, then ; | and line breaks.
var statementBreak = regexp.MustCompile(`&&|\|\||[;|\r\n]`)

// navigationVerbs change or list a directory; they read no file's content.
var navigationVerbs = map[string]bool{
	"cd": true, "chdir": true, "pushd": true, "set-location": true, "sl": true,
	"push-location": true, "dir": true, "ls": true, "gci": true, "get-childitem": true, "tree": true,
}

// laterRead is a file-reading verb or form. After a directory change to an
// outside folder it would read relative to that folder, so the change counts
// as a read of it: `cd C:\secret; type key.txt` still reaches the card.
var laterRead = regexp.MustCompile(`(?i)(^|[^a-z0-9_-])(type|cat|gc|get-content|more|less|head|tail|select-string|sls|findstr|copy|cp|copy-item|move|mv|move-item|import-csv|import-clixml|get-filehash|format-hex|readall\w*|open)([^a-z0-9_-]|$)|\[(system\.)?io\.file\]|<`)

// pathListReason names every path, up to a bound, so one cannot hide behind three.
func pathListReason(prefix string, paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) > 12 {
		paths = append(paths[:12], fmt.Sprintf("and %d more", len(paths)-12))
	}
	return prefix + strings.Join(paths, ", ")
}

// statementVerb is a statement's command word, lower-cased, without a call
// operator, a leading parenthesis or quotes.
func statementVerb(statement string) string {
	fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(statement), "&( "))
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(fields[0], `"'`))
}

// outsideCommandDecision classifies each literal outside path by the statement
// it sits in (item 2fz): an argument of a directory change or listing runs,
// unless a file read follows it in the same source; any other outside path is
// a read — the card when it exists (a denied or unknown stat counts as
// existing, 2fy's rule), a plain error when it does not. Forms the guard cannot
// see — paths built at runtime, in variables — are as before: not seen.
func outsideCommandDecision(source string, s *session.Session) outsideDecision {
	outside := map[string]bool{}
	for _, path := range outsideLiteralPaths(source, s) {
		outside[strings.ToLower(path)] = true
	}
	if len(outside) == 0 {
		return outsideDecision{}
	}
	cards, missing := []string{}, []string{}
	seen := map[string]bool{}
	bounds := statementBreak.FindAllStringIndex(source, -1)
	start := 0
	for index := 0; index <= len(bounds); index++ {
		end, rest := len(source), ""
		if index < len(bounds) {
			end, rest = bounds[index][0], source[bounds[index][1]:]
		}
		statement := source[start:end]
		if index < len(bounds) {
			start = bounds[index][1]
		}
		navigation := navigationVerbs[statementVerb(statement)] && !laterRead.MatchString(rest)
		for _, path := range outsideLiteralPaths(statement, s) {
			key := strings.ToLower(path)
			if !outside[key] || seen[key] || navigation {
				continue
			}
			seen[key] = true
			if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, path)
			} else {
				cards = append(cards, path)
			}
		}
	}
	return outsideDecision{
		card:    pathListReason("names a path outside the folder: ", cards),
		missing: pathListReason("no such file or directory outside the folder: ", missing),
	}
}
