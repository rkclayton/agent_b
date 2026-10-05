package agent

import "strings"

// Item 2jg (e): every tool error carries a CLASS, so the telemetry filter has
// something to send that is not free text.
//
// The class is derived at the one place a tool result is built, from the text
// the tool already produced. That is not ideal — a class each tool declares
// would be better — but it is honest about what exists: there is one
// `CallDetail.Err`, thirteen tools, and no error taxonomy anywhere in the
// product today. Deriving here gives the filter a fixed vocabulary now, and the
// next item can push the classification down into the tools without changing
// what leaves the machine.
//
// The vocabulary is docs/TELEMETRY.md's, and the list is closed: anything this
// cannot place is `internal`, never the message.
const (
	ClassExitNonzero = "exit_nonzero"
	ClassInvalidArgs = "invalid_args"
	ClassNotFound    = "not_found"
	ClassTimeout     = "timeout"
	ClassDenied      = "denied"
	ClassTooLarge    = "too_large"
	ClassInternal    = "internal"
)

// classPatterns is ordered: the first match wins, so the specific reasons are
// listed before the general ones.
var classPatterns = []struct {
	class  string
	needle []string
}{
	{ClassExitNonzero, []string{"command failed\nexit="}},
	{ClassDenied, []string{"refused", "not allowed", "outside the workspace", "denied", "blocked", "permission", "unauthorized", "forbidden", "guard", "401", "403", "canceled", "cancelled"}},
	{ClassNotFound, []string{"no such file", "no such host", "cannot find", "not found", "does not exist", "connection refused", "404", "unavailable"}},
	{ClassTimeout, []string{"timeout", "timed out", "deadline exceeded"}},
	{ClassTooLarge, []string{"too large", "result exceeds", "limit reached", "max bytes"}},
	{ClassInvalidArgs, []string{"is required", "must be", "invalid argument", "unknown tool", "bad request", "note too long", "not one of", "invalid character", "unmarshal", "malformed", "decode", "parse", "not configured", "no connection"}},
}

// ToolErrorClass places one failed tool result. It is called only when the
// result is not ok; a successful call has no class, and the field is absent
// rather than empty.
func ToolErrorClass(content string) string {
	lowered := strings.ToLower(content)
	for _, entry := range classPatterns {
		for _, needle := range entry.needle {
			if strings.Contains(lowered, needle) {
				return entry.class
			}
		}
	}
	return ClassInternal
}
