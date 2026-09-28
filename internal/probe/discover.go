package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"harness/internal/config"
	"harness/internal/llm"
)

type DiscoveryAttempt struct {
	BaseURL string `json:"base_url"`
	Allowed bool   `json:"allowed"`
	Result  string `json:"result"`
	// Item 2nb (a) and (b): what the address actually DID, so the walk can stop on
	// the typed one and the sheet can say what each attempt found as it lands.
	// Status is the HTTP status when the address answered at all, and zero when
	// nothing answered.
	Status   int  `json:"status,omitempty"`
	Answered bool `json:"answered,omitempty"`
	Models   int  `json:"models,omitempty"`
}

type DiscoveryResult struct {
	BaseURL  string             `json:"base_url"`
	Models   []string           `json:"models"`
	Attempts []DiscoveryAttempt `json:"attempts"`
	// NeedsKey is item 2nb (a): the typed address answered 401 or 403. That is not a
	// failure to find a server — it is a server saying who it is, and the next move is
	// the operator's: paste a key and Test again.
	NeedsKey bool `json:"needs_key,omitempty"`
	// NoModelAPI is (a)'s 404 case: something answered, and it is not a model API.
	NoModelAPI bool `json:"no_model_api,omitempty"`
	// TypedAddress is the address whose port the operator typed, once it has answered.
	// (a): the typed address wins, so a walk that reaches it stops there.
	TypedAddress string `json:"typed_address,omitempty"`
}

// answeredStatus reports the HTTP status an attempt's error carries, or zero when the
// address did not answer at all. An address that answers 401 has answered: the walk
// used to treat that as a failure and move on to another port, which is how the
// operator's typed :8080 was abandoned in favour of an :11434 that listed nothing.
func answeredStatus(err error) int {
	var shape *llm.ResponseShapeError
	if errors.As(err, &shape) {
		return shape.Status
	}
	return 0
}

// DiscoverEndpoint tries only shapes derived from the host the operator typed.
// Each candidate gets a short bound; the ordinary full probe runs only after a
// model-list endpoint has answered.
//
// Item 2nb (a): THE TYPED ADDRESS WINS. A port the operator typed that answers at all
// ends the walk, whatever it answers — 200, 401, 403, 404 — because he typed it on
// purpose and a walk that silently moves to another port rewrites his intent. Only an
// address that does not answer at all (refused, timed out, no route) lets the walk go
// on. And an answer of ZERO models never beats an answer with models: his :11434
// answered `{"data":null}` and would otherwise have won over the :8080 that had three.
func DiscoverEndpoint(ctx context.Context, connection *config.Connection) (DiscoveryResult, error) {
	candidates, host, typedPort, parseErr := discoveryCandidates(connection.BaseURL)
	result := DiscoveryResult{}
	if parseErr != nil {
		result.Attempts = append(result.Attempts, DiscoveryAttempt{BaseURL: strings.TrimSpace(connection.BaseURL), Allowed: false, Result: parseErr.Error()})
	}
	if host == "" {
		reason := "no host was found"
		if parseErr != nil {
			reason = parseErr.Error()
		}
		return result, &ConnectionError{Friendly: fmt.Sprintf("base_url %q is malformed: %s.", strings.TrimSpace(connection.BaseURL), firstLine(reason, "no host was found")), Detail: "discovery refused: no operator-typed host"}
	}
	// (a): the candidates are split by PORT. Every path on the port he typed is tried
	// first; only if that port says nothing at all does the walk go looking elsewhere.
	// The two halves matter because a 404 is about a PATH, not about the server: typing
	// host:8080/wrong must still find host:8080/v1/models, which is what item 2l1's
	// path discovery is for. What (a) forbids is LEAVING the port he typed once it has
	// answered.
	typedCandidates, otherCandidates := []string{}, []string{}
	for _, baseURL := range candidates {
		if typedPort != "" && candidateUsesPort(baseURL, typedPort) {
			typedCandidates = append(typedCandidates, baseURL)
		} else {
			otherCandidates = append(otherCandidates, baseURL)
		}
	}

	// An address that answered with no models is remembered, not taken: it is the
	// fallback if nothing better answers.
	emptyAnswer := ""
	typedAnswered, typedStatus := false, 0
	try := func(baseURL string, typed bool) (*DiscoveryResult, error, bool) {
		candidate := *connection
		candidate.BaseURL = baseURL
		if candidate.RequestTimeoutS == 0 || candidate.RequestTimeoutS > 2 {
			candidate.RequestTimeoutS = 2
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		models, err := llm.New(&candidate).Models(attemptCtx)
		cancel()
		attempt := DiscoveryAttempt{BaseURL: baseURL, Allowed: true}
		if err != nil {
			status := answeredStatus(err)
			attempt.Status, attempt.Answered = status, status > 0
			switch {
			case status == 401 || status == 403:
				attempt.Result = "wants an API key"
			case status == 404:
				attempt.Result = "answered, but no model API at this path"
			case status > 0:
				attempt.Result = fmt.Sprintf("answered HTTP %d", status)
			default:
				attempt.Result = firstLine(err.Error(), "failed")
			}
			result.Attempts = append(result.Attempts, attempt)
			if typed && attempt.Answered {
				typedAnswered, typedStatus = true, status
				result.TypedAddress = baseURL
				// A key is needed whatever the path, so this one stops the walk here.
				if status == 401 || status == 403 {
					result.NeedsKey = true
					return &result, &ConnectionError{
						Friendly: fmt.Sprintf("%s wants an API key — paste it and Test again.", baseURL),
						Detail:   fmt.Sprintf("discovery stopped at the operator-typed address %s: HTTP %d", baseURL, status),
					}, true
				}
			}
			return nil, nil, false
		}
		models = uniqueModels(models)
		attempt.Answered, attempt.Status, attempt.Models = true, 200, len(models)
		attempt.Result = fmt.Sprintf("model list answered with %d model(s)", len(models))
		result.Attempts = append(result.Attempts, attempt)
		if typed {
			typedAnswered, typedStatus = true, 200
			result.TypedAddress = baseURL
		}
		if len(models) == 0 {
			// (a): zero models never beats models, so this is remembered and the walk
			// goes on through the remaining paths.
			if emptyAnswer == "" {
				emptyAnswer = baseURL
			}
			return nil, nil, false
		}
		result.BaseURL, result.Models = baseURL, models
		return &result, nil, true
	}

	for _, baseURL := range typedCandidates {
		if _, err, done := try(baseURL, true); done {
			return result, err
		}
	}
	// (a): the typed port answered but listed nothing usable. The walk stops here
	// rather than quietly moving to another port and rewriting what he typed.
	if typedAnswered {
		if emptyAnswer != "" {
			result.BaseURL, result.Models = emptyAnswer, []string{}
			return result, nil
		}
		result.NoModelAPI = typedStatus == 404
		tried := pathsTried(result.Attempts, typedPort)
		return result, &ConnectionError{
			Friendly: fmt.Sprintf("No model API at %s; tried %s.", result.TypedAddress, strings.Join(tried, " and ")),
			Detail:   fmt.Sprintf("discovery stopped at the operator-typed port %s: HTTP %d", typedPort, typedStatus),
		}
	}
	for _, baseURL := range otherCandidates {
		if _, err, done := try(baseURL, false); done {
			return result, err
		}
	}
	if emptyAnswer != "" {
		// Something answered, with nothing loaded. That is a real answer and the
		// operator can act on it, so it is reported rather than thrown away.
		result.BaseURL, result.Models = emptyAnswer, []string{}
		return result, nil
	}
	detail := make([]string, 0, len(result.Attempts))
	for _, attempt := range result.Attempts {
		detail = append(detail, attempt.BaseURL+": "+attempt.Result)
	}
	return result, &ConnectionError{Friendly: fmt.Sprintf("No model API answered for %s; tried %s.", host, strings.Join(detail, "; ")), Detail: "discovery exhausted operator-typed host " + host}
}

// candidateUsesPort reports whether a candidate address carries the port the operator
// typed. The candidate list holds both spellings of each address, so this matches the
// port rather than the whole string.
func candidateUsesPort(baseURL, port string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil {
		return false
	}
	if parsed.Port() == port {
		return true
	}
	// A typed address with no explicit port carries the scheme's default.
	switch {
	case parsed.Port() == "" && parsed.Scheme == "https" && port == "443":
		return true
	case parsed.Port() == "" && parsed.Scheme == "http" && port == "80":
		return true
	}
	return false
}

// pathsTried names the model-list paths actually attempted on the typed address, for
// (a)'s 404 message.
func pathsTried(attempts []DiscoveryAttempt, typedPort string) []string {
	seen := map[string]bool{}
	paths := []string{}
	for _, attempt := range attempts {
		if typedPort != "" && !candidateUsesPort(attempt.BaseURL, typedPort) {
			continue
		}
		path := strings.TrimRight(attempt.BaseURL, "/")
		if strings.HasSuffix(strings.ToLower(path), "/v1") {
			path += "/models"
		} else {
			path += "/v1/models"
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

func discoveryCandidates(raw string) ([]string, string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", "", fmt.Errorf("it is empty")
	}
	var parsed *url.URL
	var err error
	lower := strings.ToLower(raw)
	malformedScheme := (strings.HasPrefix(lower, "https:/") && !strings.HasPrefix(lower, "https://")) || (strings.HasPrefix(lower, "http:/") && !strings.HasPrefix(lower, "http://"))
	if malformedScheme {
		return nil, "", "", fmt.Errorf("the scheme needs two slashes (for example, https://host)")
	} else if strings.Contains(raw, "://") {
		parsed, err = url.Parse(raw)
	} else {
		parsed, err = url.Parse("//" + raw)
	}
	if parsed == nil || parsed.Hostname() == "" || parsed.User != nil {
		if err == nil {
			err = fmt.Errorf("no host was found")
		}
		return nil, "", "", err
	}
	host := parsed.Hostname()
	if net.ParseIP(host) == nil && strings.ContainsAny(host, " /?#") {
		return nil, "", "", fmt.Errorf("host %q is invalid", host)
	}
	seen := map[string]bool{}
	values := []string{}
	add := func(value string) {
		value = strings.TrimRight(value, "/")
		if value == "" || seen[strings.ToLower(value)] {
			return
		}
		seen[strings.ToLower(value)] = true
		values = append(values, value)
		if !strings.HasSuffix(strings.ToLower(value), "/v1") {
			values = append(values, value+"/v1")
			seen[strings.ToLower(value+"/v1")] = true
		}
	}
	if !malformedScheme && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		add(raw)
	}
	hostForURL := host
	if strings.Contains(host, ":") {
		hostForURL = "[" + host + "]"
	}
	ports := []string{"443", "80", "8000", "8080", "11434", "1234", "5000"}
	if parsed.Port() != "" {
		ports = append([]string{parsed.Port()}, ports...)
	}
	for _, scheme := range []string{"https", "http"} {
		for _, port := range ports {
			add(fmt.Sprintf("%s://%s:%s", scheme, hostForURL, port))
		}
	}
	return values, host, parsed.Port(), err
}

func uniqueModels(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
