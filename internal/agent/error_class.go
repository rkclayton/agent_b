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
	ClassNotFound       = "not_found"
	ClassPermission     = "permission"
	ClassRefusedByGuard = "refused_by_guard"
	ClassTimeout        = "timeout"
	ClassNetwork        = "network"
	ClassParse          = "parse"
	ClassTooLarge       = "too_large"
	ClassCancelled      = "cancelled"
	ClassBadRequest     = "bad_request"
	ClassUnavailable    = "unavailable"
	ClassInternal       = "internal"
)

// classPatterns is ordered: the first match wins, so the specific reasons are
// listed before the general ones.
var classPatterns = []struct {
	class  string
	needle []string
}{
	{ClassRefusedByGuard, []string{"refused", "not allowed", "outside the workspace", "denied by policy", "guard"}},
	{ClassPermission, []string{"access is denied", "permission denied", "unauthorized", "forbidden", "403", "401"}},
	{ClassNotFound, []string{"no such file", "cannot find", "not found", "does not exist", "404"}},
	{ClassTimeout, []string{"timeout", "timed out", "deadline exceeded"}},
	{ClassCancelled, []string{"canceled", "cancelled", "context canceled"}},
	{ClassTooLarge, []string{"too large", "exceeded", "limit reached", "max bytes"}},
	{ClassNetwork, []string{"dial tcp", "connection refused", "no such host", "eof", "network", "tls", "request failed"}},
	{ClassParse, []string{"parse", "invalid character", "unmarshal", "malformed", "decode"}},
	{ClassBadRequest, []string{"is required", "must be", "invalid argument", "unknown tool", "bad request"}},
	{ClassUnavailable, []string{"unavailable", "disabled", "not configured", "no connection"}},
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
