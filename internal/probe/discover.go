package probe

import (
	"context"
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
}

type DiscoveryResult struct {
	BaseURL  string             `json:"base_url"`
	Models   []string           `json:"models"`
	Attempts []DiscoveryAttempt `json:"attempts"`
}

// DiscoverEndpoint tries only shapes derived from the host the operator typed.
// Each candidate gets a short bound; the ordinary full probe runs only after a
// model-list endpoint has answered.
func DiscoverEndpoint(ctx context.Context, profile *config.Profile) (DiscoveryResult, error) {
	candidates, host, parseErr := discoveryCandidates(profile.BaseURL)
	result := DiscoveryResult{}
	if parseErr != nil {
		result.Attempts = append(result.Attempts, DiscoveryAttempt{BaseURL: strings.TrimSpace(profile.BaseURL), Allowed: false, Result: parseErr.Error()})
	}
	if host == "" {
		reason := "no host was found"
		if parseErr != nil {
			reason = parseErr.Error()
		}
		return result, &ConnectionError{Friendly: fmt.Sprintf("base_url %q is malformed: %s.", strings.TrimSpace(profile.BaseURL), firstLine(reason, "no host was found")), Detail: "discovery refused: no operator-typed host"}
	}
	for _, baseURL := range candidates {
		candidate := *profile
		candidate.BaseURL = baseURL
		if candidate.RequestTimeoutS == 0 || candidate.RequestTimeoutS > 2 {
			candidate.RequestTimeoutS = 2
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
		models, err := llm.New(&candidate).Models(attemptCtx)
		cancel()
		attempt := DiscoveryAttempt{BaseURL: baseURL, Allowed: true}
		if err != nil {
			attempt.Result = firstLine(err.Error(), "failed")
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		models = uniqueModels(models)
		attempt.Result = fmt.Sprintf("model list answered with %d model(s)", len(models))
		result.Attempts = append(result.Attempts, attempt)
		result.BaseURL, result.Models = baseURL, models
		return result, nil
	}
	detail := make([]string, 0, len(result.Attempts))
	for _, attempt := range result.Attempts {
		detail = append(detail, attempt.BaseURL+": "+attempt.Result)
	}
	return result, &ConnectionError{Friendly: fmt.Sprintf("No model API answered for %s; tried %s.", host, strings.Join(detail, "; ")), Detail: "discovery exhausted operator-typed host " + host}
}

func discoveryCandidates(raw string) ([]string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, "", fmt.Errorf("it is empty")
	}
	var parsed *url.URL
	var err error
	lower := strings.ToLower(raw)
	malformedScheme := (strings.HasPrefix(lower, "https:/") && !strings.HasPrefix(lower, "https://")) || (strings.HasPrefix(lower, "http:/") && !strings.HasPrefix(lower, "http://"))
	if malformedScheme {
		return nil, "", fmt.Errorf("the scheme needs two slashes (for example, https://host)")
	} else if strings.Contains(raw, "://") {
		parsed, err = url.Parse(raw)
	} else {
		parsed, err = url.Parse("//" + raw)
	}
	if parsed == nil || parsed.Hostname() == "" || parsed.User != nil {
		if err == nil {
			err = fmt.Errorf("no host was found")
		}
		return nil, "", err
	}
	host := parsed.Hostname()
	if net.ParseIP(host) == nil && strings.ContainsAny(host, " /?#") {
		return nil, "", fmt.Errorf("host %q is invalid", host)
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
	return values, host, err
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
