package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"harness/internal/config"
	"harness/internal/session"
)

var serviceRequestHeaders = map[string]bool{
	"Accept":          true,
	"Content-Type":    true,
	"Idempotency-Key": true,
	"If-Match":        true,
	"If-None-Match":   true,
}

var serviceResponseHeaders = []string{
	"Content-Type", "ETag", "Last-Modified", "Retry-After", "X-Request-Id",
}

type cachedServiceCredential struct {
	authorization string
	token         string
	validUntil    time.Time
}

type CallService struct {
	mu       sync.Mutex
	services map[string]config.Service
	cache    map[string]cachedServiceCredential
	now      func() time.Time
}

func NewCallService(services map[string]config.Service) *CallService {
	tool := &CallService{cache: map[string]cachedServiceCredential{}, now: time.Now}
	tool.setServices(services)
	return tool
}

func (*CallService) Name() string { return "call_service" }

func (*CallService) Description() string {
	return "Call one configured internal service with its configured identity. Use only a relative path and an allowed method/header. Responses use UTF-8 byte windows; when cursor.more is true, repeat the same request with cursor.next_offset as offset. Never supply Authorization."
}

func (*CallService) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"service": map[string]any{"type": "string", "description": "Configured service name"},
			"method":  map[string]any{"type": "string", "description": "HTTP method allowed by the service"},
			"path":    map[string]any{"type": "string", "description": "Relative path within the configured base_url"},
			"query":   map[string]any{"type": "object", "description": "Query parameters", "additionalProperties": true},
			"body":    map[string]any{"description": "JSON request body"},
			"headers": map[string]any{"type": "object", "description": "Optional Accept, Content-Type, If-Match, If-None-Match, or Idempotency-Key values", "additionalProperties": map[string]any{"type": "string"}},
			"offset":  map[string]any{"type": "integer", "description": "One-based response-body byte offset for a repeated request", "default": 1},
			"limit":   map[string]any{"type": "integer", "description": "Maximum response-body bytes, capped by the configured service maximum"},
		},
		"required": []string{"service", "method", "path"},
	}
}

func (c *CallService) Configure(cfg config.Config) {
	c.mu.Lock()
	c.setServicesLocked(cfg.Services)
	c.cache = map[string]cachedServiceCredential{}
	c.mu.Unlock()
}

func (c *CallService) setServices(services map[string]config.Service) {
	c.mu.Lock()
	c.setServicesLocked(services)
	c.mu.Unlock()
}

func (c *CallService) setServicesLocked(services map[string]config.Service) {
	c.services = make(map[string]config.Service, len(services))
	for name, service := range services {
		service.AllowedMethods = append([]string(nil), service.AllowedMethods...)
		c.services[name] = service
		if service.RequireConfirmation {
			log.Printf("call_service: service=%q require_confirmation=true is recorded but not enforced in this release", name)
		}
	}
}

func (c *CallService) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	detail := c.CallDetailed(ctx, item, args)
	return detail.Content, detail.Err
}

func (c *CallService) CallDetailed(ctx context.Context, _ *session.Session, args map[string]any) (detail CallDetail) {
	serviceName, ok := requiredString(args, "service")
	if !ok {
		detail.Err = fmt.Errorf("service is required")
		return detail
	}
	method, ok := requiredString(args, "method")
	if !ok {
		detail.Err = fmt.Errorf("method is required")
		return detail
	}
	method = strings.ToUpper(method)
	requestedPath, ok := args["path"].(string)
	if !ok {
		detail.Err = fmt.Errorf("path is required")
		return detail
	}

	service, found := c.service(serviceName)
	if !found {
		detail.Err = fmt.Errorf("unknown service %q", serviceName)
		return detail
	}
	if !methodAllowed(method, service.AllowedMethods) {
		detail.Err = fmt.Errorf("method %s is not allowed for service %q", method, serviceName)
		return detail
	}
	target, err := resolveServiceTarget(service.BaseURL, requestedPath)
	if err != nil {
		detail.Err = err
		return detail
	}
	if err := addServiceQuery(target, args["query"]); err != nil {
		detail.Err = err
		return detail
	}
	headers, err := parseServiceHeaders(args["headers"])
	if err != nil {
		detail.Err = err
		return detail
	}
	offset := number(args["offset"], 1)
	if offset < 1 {
		detail.Err = fmt.Errorf("offset must be at least 1")
		return detail
	}
	limit := number(args["limit"], service.MaxBodyKB<<10)
	if limit < 1 {
		detail.Err = fmt.Errorf("limit must be positive")
		return detail
	}
	limit = min(limit, service.MaxBodyKB<<10)

	var body io.Reader
	if value, present := args["body"]; present {
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			detail.Err = fmt.Errorf("encode body: %w", encodeErr)
			return detail
		}
		body = bytes.NewReader(encoded)
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/json")
		}
	}

	authorization, token, operatorContext, err := c.authorization(ctx, serviceName, service)
	if err != nil {
		detail.Err = err
		return detail
	}
	detail.OperatorContext = operatorContext
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		detail.Err = fmt.Errorf("build service request: %w", err)
		return detail
	}
	request.Header = headers
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}

	started := c.now()
	client := serviceHTTPClient(service)
	response, err := client.Do(request)
	duration := c.now().Sub(started).Milliseconds()
	if err != nil {
		detail.Err = fmt.Errorf("service request failed: %w", err)
		log.Printf("call_service: service=%q method=%s path=%q duration_ms=%d error=%q", serviceName, method, requestedPath, duration, detail.Err)
		return detail
	}
	defer response.Body.Close()

	window, err := readServiceWindow(response.Body, offset, limit)
	if err != nil {
		detail.Err = err
		return detail
	}
	cleanBody := redactServiceCredential(window.Content, authorization, token)
	outputBody := any(cleanBody)
	if offset == 1 && !window.More && json.Valid([]byte(cleanBody)) {
		var decoded any
		if json.Unmarshal([]byte(cleanBody), &decoded) == nil {
			outputBody = decoded
		}
	}
	cursor := map[string]any{"offset": window.Offset, "bytes": window.Bytes, "more": window.More}
	if window.More {
		cursor["next_offset"] = window.NextOffset
	}
	result := map[string]any{
		"status": response.StatusCode, "headers": selectedServiceHeaders(response.Header),
		"body": outputBody, "cursor": cursor, "duration_ms": duration,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		detail.Err = fmt.Errorf("encode service result: %w", err)
		return detail
	}
	detail.Content = string(encoded)
	detail.Metadata = map[string]any{
		"service": serviceName, "method": method, "path": requestedPath,
		"status": response.StatusCode, "duration_ms": duration,
		"window_offset": window.Offset, "window_bytes": window.Bytes, "more": window.More,
	}
	if window.More {
		detail.Metadata["next_offset"] = window.NextOffset
	}
	log.Printf("call_service: service=%q method=%s path=%q status=%d duration_ms=%d bytes=%d more=%t", serviceName, method, requestedPath, response.StatusCode, duration, window.Bytes, window.More)
	return detail
}

func (c *CallService) service(name string) (config.Service, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	service, ok := c.services[name]
	return service, ok
}

func (c *CallService) authorization(ctx context.Context, name string, service config.Service) (authorization, token string, operatorContext bool, err error) {
	auth := strings.TrimSpace(service.Auth)
	if auth == "none" {
		return "", "", false, nil
	}
	if strings.HasPrefix(auth, "static_bearer:") {
		value := strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(auth, "static_bearer:"))))
		if value == "" {
			return "", "", false, fmt.Errorf("auth_error: configured bearer environment variable is empty")
		}
		authorization, token, err = bearerAuthorization(value)
		return authorization, token, false, err
	}
	if !strings.HasPrefix(auth, "exec:") {
		return "", "", false, fmt.Errorf("auth_error: unsupported configured auth mode")
	}

	c.mu.Lock()
	if cached, ok := c.cache[name]; ok && c.now().Before(cached.validUntil) {
		c.mu.Unlock()
		return cached.authorization, cached.token, true, nil
	}
	c.mu.Unlock()

	argv, err := splitServiceArgv(strings.TrimSpace(strings.TrimPrefix(auth, "exec:")))
	if err != nil || len(argv) == 0 {
		return "", "", true, fmt.Errorf("auth_error: invalid credential argv")
	}
	credentialContext, cancel := context.WithTimeout(ctx, time.Duration(service.TimeoutS)*time.Second)
	defer cancel()
	command := exec.CommandContext(credentialContext, argv[0], argv[1:]...)
	output, runErr := command.Output()
	if credentialContext.Err() == context.DeadlineExceeded {
		return "", "", true, fmt.Errorf("auth_error: credential command timed out")
	}
	if runErr != nil {
		return "", "", true, fmt.Errorf("auth_error: credential command failed")
	}
	line := strings.TrimSpace(string(output))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", "", true, fmt.Errorf("auth_error: credential output must be one non-empty line")
	}
	expires := time.Time{}
	if strings.HasPrefix(line, "{") {
		var value struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		if json.Unmarshal([]byte(line), &value) != nil || strings.TrimSpace(value.Token) == "" {
			return "", "", true, fmt.Errorf("auth_error: credential JSON is invalid")
		}
		line = strings.TrimSpace(value.Token)
		if value.ExpiresAt != "" {
			expires, err = time.Parse(time.RFC3339, value.ExpiresAt)
			if err != nil {
				return "", "", true, fmt.Errorf("auth_error: credential expires_at must be RFC3339")
			}
		}
	}
	authorization, token, err = bearerAuthorization(line)
	if err != nil {
		return "", "", true, err
	}
	validUntil := c.now().Add(5 * time.Minute)
	if !expires.IsZero() {
		validUntil = expires.Add(-time.Minute)
	}
	if c.now().Before(validUntil) {
		c.mu.Lock()
		c.cache[name] = cachedServiceCredential{authorization: authorization, token: token, validUntil: validUntil}
		c.mu.Unlock()
	}
	return authorization, token, true, nil
}

func bearerAuthorization(value string) (authorization, token string, err error) {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "Bearer ") {
		token = strings.TrimSpace(value[7:])
	} else {
		token = value
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", "", fmt.Errorf("auth_error: bearer token is invalid")
	}
	return "Bearer " + token, token, nil
}

func splitServiceArgv(value string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			args = append(args, current.String())
			current.Reset()
			started = false
		}
	}
	for _, char := range value {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
			started = true
		case char == '\'' || char == '"':
			quote = char
			started = true
		case char == ' ' || char == '\t':
			flush()
		default:
			current.WriteRune(char)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	return args, nil
}

func resolveServiceTarget(baseURL, requested string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("configured service base_url is invalid")
	}
	raw := strings.TrimSpace(requested)
	relative, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("path is invalid")
	}
	lowerRaw := strings.ToLower(raw)
	if raw == "" || relative.IsAbs() || relative.Host != "" || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) || relative.RawQuery != "" || relative.Fragment != "" {
		return nil, fmt.Errorf("path must be relative to the configured base_url")
	}
	if strings.Contains(relative.Path, `\`) || strings.Contains(lowerRaw, "%2f") || strings.Contains(lowerRaw, "%5c") {
		return nil, fmt.Errorf("path escape outside configured base_url refused")
	}
	for _, segment := range strings.Split(relative.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, fmt.Errorf("path escape outside configured base_url refused")
		}
	}
	root := *base
	root.Path = strings.TrimSuffix(root.Path, "/") + "/"
	root.RawPath = ""
	target := root.ResolveReference(relative)
	if err := validateResolvedServiceTarget(&root, target); err != nil {
		return nil, err
	}
	return target, nil
}

func validateResolvedServiceTarget(base, target *url.URL) error {
	if target.Scheme != base.Scheme || !strings.EqualFold(target.Host, base.Host) || !strings.HasPrefix(target.Path, base.Path) {
		return fmt.Errorf("path escape outside configured base_url refused")
	}
	return nil
}

func addServiceQuery(target *url.URL, raw any) error {
	if raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("query must be an object")
	}
	query := target.Query()
	for key, value := range values {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("query names cannot be empty")
		}
		switch item := value.(type) {
		case []any:
			for _, element := range item {
				encoded, err := serviceScalar(element)
				if err != nil {
					return fmt.Errorf("query.%s: %w", key, err)
				}
				query.Add(key, encoded)
			}
		default:
			encoded, err := serviceScalar(item)
			if err != nil {
				return fmt.Errorf("query.%s: %w", key, err)
			}
			query.Add(key, encoded)
		}
	}
	target.RawQuery = query.Encode()
	return nil
}

func serviceScalar(value any) (string, error) {
	switch item := value.(type) {
	case string:
		return item, nil
	case float64:
		return strconv.FormatFloat(item, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(item), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("values must be strings, numbers, booleans, null, or arrays of those")
	}
}

func parseServiceHeaders(raw any) (http.Header, error) {
	headers := http.Header{}
	if raw == nil {
		return headers, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("headers must be an object")
	}
	for name, rawValue := range values {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if !serviceRequestHeaders[canonical] {
			return nil, fmt.Errorf("header %q is not allowed; Authorization is always configured by the service", name)
		}
		value, ok := rawValue.(string)
		if !ok || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("header %q must be a single-line string", name)
		}
		headers.Set(canonical, value)
	}
	return headers, nil
}

func serviceHTTPClient(service config.Service) *http.Client {
	base, _ := url.Parse(service.BaseURL)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	return &http.Client{
		Transport: transport,
		Timeout:   time.Duration(service.TimeoutS) * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			root := *base
			root.Path = strings.TrimSuffix(root.Path, "/") + "/"
			root.RawPath = ""
			return validateResolvedServiceTarget(&root, request.URL)
		},
	}
}

func readServiceWindow(body io.Reader, offset, limit int) (byteWindow, error) {
	if offset > 1 {
		skipped, err := io.CopyN(io.Discard, body, int64(offset-1))
		if err != nil {
			return byteWindow{}, fmt.Errorf("offset %d exceeds response body byte length %d", offset, skipped)
		}
	}
	data, err := io.ReadAll(io.LimitReader(body, int64(limit+utf8.UTFMax)))
	if err != nil {
		return byteWindow{}, fmt.Errorf("read service response: %w", err)
	}
	end := 0
	for end < len(data) && end < limit {
		runeValue, width := utf8.DecodeRune(data[end:])
		if runeValue == utf8.RuneError && width == 1 {
			return byteWindow{}, fmt.Errorf("response body is not valid UTF-8 at byte offset %d", offset+end)
		}
		if end+width > limit {
			break
		}
		end += width
	}
	if len(data) > 0 && end == 0 {
		_, width := utf8.DecodeRune(data)
		return byteWindow{}, fmt.Errorf("limit %d is too small for the %d-byte UTF-8 rune at offset %d", limit, width, offset)
	}
	more := len(data) > end
	window := byteWindow{Content: string(data[:end]), Offset: offset, Bytes: end, More: more}
	if window.More {
		window.NextOffset = offset + end
	}
	return window, nil
}

func selectedServiceHeaders(headers http.Header) map[string]string {
	selected := map[string]string{}
	for _, name := range serviceResponseHeaders {
		if value := headers.Get(name); value != "" {
			selected[strings.ToLower(name)] = value
		}
	}
	return selected
}

func redactServiceCredential(value, authorization, token string) string {
	if authorization != "" {
		value = strings.ReplaceAll(value, authorization, "[redacted]")
	}
	if token != "" {
		value = strings.ReplaceAll(value, token, "[redacted]")
	}
	return value
}

func methodAllowed(method string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), method) {
			return true
		}
	}
	return false
}

func requiredString(args map[string]any, key string) (string, bool) {
	value, ok := args[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}
