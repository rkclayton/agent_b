package credential

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Item 2nv (f): DESTINATION ENFORCEMENT, and nothing else. This file knows nothing about
// how a secret is stored or which provider produced it — it is given an approved origin
// and a URL, and it answers. Every credential kind passes through it before a request is
// built: a stored key, a helper's output, and (stage 2) a signed-in provider's token.
//
// An origin is scheme + host + PORT, and the port is why this exists rather than the host
// check that was here before: a credential pinned by host alone followed a same-host
// redirect to plain http, which is the operator's key on the wire in clear.

// NormalizeOrigin reads an origin the operator approved and returns it in the one form
// everything else compares against. HTTPS only; the port is always explicit, so 443 and
// an omitted port are the same origin and 8443 is not.
func NormalizeOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("an origin is required: https://host or https://host:port")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("the origin is not a URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("a credential goes only to https, and %q is %q", trimmed, parsed.Scheme)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("an origin carries no credentials of its own")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("the origin names no host")
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("the origin's port %q is not a port", port)
	}
	return "https://" + host + ":" + port, nil
}

// OriginOf is the origin a request is actually going to.
func OriginOf(target *url.URL) string {
	if target == nil {
		return ""
	}
	scheme := strings.ToLower(target.Scheme)
	port := target.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + strings.ToLower(target.Hostname()) + ":" + port
}

// AllowsOrigin answers whether a credential approved for origin may be attached to a
// request to target. Nothing is approximate: the three parts match or they do not, and
// the refusal names the approved origin so the operator can see which one it was.
func AllowsOrigin(origin string, target *url.URL) error {
	approved, err := NormalizeOrigin(origin)
	if err != nil {
		return fmt.Errorf("the approved origin is unreadable: %w", err)
	}
	going := OriginOf(target)
	if going != approved {
		return fmt.Errorf("the credential is approved for %s and this request goes to %s, so it was not attached", approved, going)
	}
	return nil
}
