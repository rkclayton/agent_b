package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"harness/internal/attachment"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/quietproc"
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
	headers       http.Header
	validUntil    time.Time
}

type CallService struct {
	mu       sync.Mutex
	services map[string]config.Service
	cache    map[string]cachedServiceCredential
	now      func() time.Time
	listener string
	change   func(ConnectorChange) error
	// Item 2nv: the named-credential store, and a client a test can hand in so an
	// https stub can be reached without loosening anything in the product path.
	vault      *credential.Vault
	testClient *http.Client
	providers  map[string]TokenProvider
}

// SetHTTPClientForTest lets a case dial its own TLS stub. Nothing in the product sets it.
func (c *CallService) SetHTTPClientForTest(client *http.Client) {
	c.mu.Lock()
	c.testClient = client
	c.mu.Unlock()
}

type ConnectorChange struct {
	Operation string
	Name      string
	Service   config.Service
	// Document is the OpenAPI document this change imported, carried from the approval
	// to the snapshot the server writes. It is never part of the configuration.
	Document []byte
}

func NewCallService(services map[string]config.Service) *CallService {
	tool := &CallService{cache: map[string]cachedServiceCredential{}, providers: map[string]TokenProvider{}, now: time.Now}
	tool.setServices(services)
	return tool
}

func (*CallService) Name() string { return "call_service" }

func (*CallService) Description() string {
	return "Call a registered service, or when the operator asks, draft a connector add/edit/remove for approval. Never propose a connector unsolicited or ask for a token when an auth helper exists."
}

// Schema is built from the CONFIGURED CONNECTORS each time it is asked for. Item 2nr (d):
// a connector that has imported a document lists its enabled operations and their
// parameters here, so the model needs no prose note and no second tool. The definition is
// assembled per request (internal/agent/run.go calls Registry.Schemas), so a connector
// approved mid-chat is callable on the next turn; tool order is the registry's and is
// untouched.
func (c *CallService) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"connector": map[string]any{"type": "object", "description": "Operator-requested connector change: operation add, edit, or remove; entry has name, url, kind (mcp or http), auth, allowed_methods for http, and openapi {document, operations} to import an OpenAPI document"},
			"operation": map[string]any{"type": "string", "description": c.operationsDescription()},
			"accept":    map[string]any{"type": "string", "description": "One response content type declared by the chosen imported operation"},
			"params":    map[string]any{"type": "object", "description": "Parameters for operation, by the document's own names", "additionalProperties": true},
			"service":   map[string]any{"type": "string", "description": "Registered service name or absolute HTTP(S) URL"},
			"method":    map[string]any{"type": "string", "description": "HTTP method allowed by the service"},
			"path":      map[string]any{"type": "string", "description": "Relative path for a registered service, or an absolute URL whose host must match that service"},
			"query":     map[string]any{"type": "object", "description": "Query parameters", "additionalProperties": true},
			"body":      map[string]any{"description": "JSON request body"},
			"headers":   map[string]any{"type": "object", "description": "Optional Accept, Content-Type, If-Match, If-None-Match, or Idempotency-Key values", "additionalProperties": map[string]any{"type": "string"}},
			"offset":    map[string]any{"type": "integer", "description": "One-based response-body byte offset for a repeated request", "default": 1},
			"limit":     map[string]any{"type": "integer", "description": "Maximum response-body bytes, capped by the configured service maximum"},
		},
		"anyOf": []any{map[string]any{"required": []string{"connector"}}, map[string]any{"required": []string{"service", "operation"}}, map[string]any{"required": []string{"service", "method"}}},
	}
}

func (c *CallService) SetConnectorWriter(change func(ConnectorChange) error) { c.change = change }

func ParseConnectorChange(args map[string]any) (ConnectorChange, bool, error) {
	raw, present := args["connector"]
	if !present {
		return ConnectorChange{}, false, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return ConnectorChange{}, true, fmt.Errorf("connector must be an object")
	}
	op, _ := value["operation"].(string)
	op = strings.ToLower(strings.TrimSpace(op))
	if op != "add" && op != "edit" && op != "remove" {
		return ConnectorChange{}, true, fmt.Errorf("connector.operation must be add, edit, or remove")
	}
	entry, ok := value["entry"].(map[string]any)
	if !ok {
		return ConnectorChange{}, true, fmt.Errorf("connector.entry must be an object")
	}
	name, _ := entry["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return ConnectorChange{}, true, fmt.Errorf("connector.entry.name is required")
	}
	change := ConnectorChange{Operation: op, Name: name}
	if op == "remove" {
		return change, true, nil
	}
	change.Service.BaseURL, _ = entry["url"].(string)
	change.Service.Kind, _ = entry["kind"].(string)
	change.Service.Auth, _ = entry["auth"].(string)
	change.Service.Kind = strings.ToLower(strings.TrimSpace(change.Service.Kind))
	if change.Service.Kind != "http" && change.Service.Kind != "mcp" {
		return ConnectorChange{}, true, fmt.Errorf("connector.entry.kind must be mcp or http")
	}
	if err := config.ValidateNewServiceAuth(change.Service.Auth); err != nil {
		return ConnectorChange{}, true, fmt.Errorf("connector.entry.auth: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(change.Service.Auth), "exec:") {
		argv, err := splitServiceArgv(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(change.Service.Auth), "exec:")))
		if err != nil || len(argv) == 0 || (!strings.EqualFold(argv[len(argv)-1], "token") && !strings.EqualFold(argv[len(argv)-1], "headers")) {
			return ConnectorChange{}, true, fmt.Errorf("connector.entry.auth: exec helper must end with token or headers")
		}
	}
	if change.Service.Kind == "mcp" {
		change.Service.AllowedMethods = []string{"POST"}
	} else if methods, ok := entry["allowed_methods"].([]any); ok {
		for _, rawMethod := range methods {
			if method, ok := rawMethod.(string); ok {
				change.Service.AllowedMethods = append(change.Service.AllowedMethods, strings.ToUpper(strings.TrimSpace(method)))
			}
		}
	}
	change.Service.TimeoutS, change.Service.MaxBodyKB = 60, 64
	// Item 2nr (b): the proposal may name an OpenAPI document and the operations to
	// enable. NOTHING IS FETCHED HERE — parsing a proposal does no I/O; the approval path
	// fetches the document once, shows it on the same card, and records the snapshot.
	if raw, present := entry["openapi"]; present && raw != nil {
		imported, ok := raw.(map[string]any)
		if !ok {
			return ConnectorChange{}, true, fmt.Errorf("connector.entry.openapi must be an object")
		}
		source, _ := imported["document"].(string)
		source = strings.TrimSpace(source)
		if source == "" {
			return ConnectorChange{}, true, fmt.Errorf("connector.entry.openapi.document is required")
		}
		operations := []string{}
		values, _ := imported["operations"].([]any)
		for _, value := range values {
			if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
				operations = append(operations, strings.TrimSpace(name))
			}
		}
		if len(operations) == 0 {
			return ConnectorChange{}, true, fmt.Errorf("connector.entry.openapi.operations must name at least one operation")
		}
		change.Service.OpenAPI = &config.ServiceOpenAPI{Source: source, Operations: operations}
	}
	return change, true, nil
}

func (c *CallService) Configure(cfg config.Config) {
	c.mu.Lock()
	c.setServicesLocked(cfg.Services)
	c.listener = cfg.Listen
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

func (c *CallService) CallDetailed(ctx context.Context, item *session.Session, args map[string]any) (detail CallDetail) {
	if change, present, err := ParseConnectorChange(args); present {
		if err != nil {
			detail.Err = err
			return detail
		}
		if c.change == nil {
			detail.Err = fmt.Errorf("connector changes are unavailable")
			return detail
		}
		// Item 2nr (b): the document the operator approved is fetched once — this reads
		// the same five-minute entry the approval card was drawn from — and the
		// operations he enabled must be operations it declares.
		if change.Service.OpenAPI != nil {
			raw, document, loadErr := LoadServiceDocument(ctx, change.Service.OpenAPI.Source)
			if loadErr != nil {
				detail.Err = loadErr
				return detail
			}
			if _, enabledErr := document.Enabled(change.Service.OpenAPI.Operations); enabledErr != nil {
				detail.Err = enabledErr
				return detail
			}
			change.Document = raw
		}
		if err := c.change(change); err != nil {
			detail.Err = err
			return detail
		}
		detail.Content = fmt.Sprintf("connector %s %s; configuration reloaded", change.Name, change.Operation)
		return detail
	}
	serviceName, ok := requiredString(args, "service")
	if !ok {
		detail.Err = fmt.Errorf("service is required")
		return detail
	}
	operationName, _ := args["operation"].(string)
	// Item 2nr (a) and (c): the operation form, and the document as the allow-list. Both
	// end as the ordinary call below — one code path from here down, which is (f).
	if service, registered := c.service(serviceName); registered && service.OpenAPI != nil {
		rewritten, err := c.operationArgs(serviceName, service, args)
		if err != nil {
			detail.Err = err
			return detail
		}
		args = rewritten
	} else if _, asked := args["operation"]; asked {
		detail.Err = fmt.Errorf("service %q has no imported document, so it takes method and path", serviceName)
		return detail
	}
	method, ok := requiredString(args, "method")
	if !ok {
		detail.Err = fmt.Errorf("method is required")
		return detail
	}
	method = strings.ToUpper(method)
	requestedPath, _ := args["path"].(string)
	requestedPath = strings.TrimSpace(requestedPath)

	service, registered := c.service(serviceName)
	var target *url.URL
	credentialHost := ""
	if registered {
		if !methodAllowed(method, service.AllowedMethods) {
			detail.Err = fmt.Errorf("method %s is not allowed for service %q", method, serviceName)
			return detail
		}
		if requestedPath == "" && service.Kind != "mcp" {
			detail.Err = fmt.Errorf("path is required for registered service %q", serviceName)
			return detail
		}
		base, baseErr := parseServiceURL(service.BaseURL)
		if baseErr != nil {
			detail.Err = fmt.Errorf("configured service base_url is invalid")
			return detail
		}
		credentialHost = base.Host
		requestedURL, absolute, parseErr := parseOptionalAbsoluteServiceURL(requestedPath)
		if parseErr != nil {
			detail.Err = parseErr
			return detail
		}
		if absolute {
			if !strings.EqualFold(requestedURL.Host, credentialHost) {
				detail.Err = credentialHostMismatch(serviceName, credentialHost, requestedURL.Host)
				return detail
			}
			target = requestedURL
		} else if requestedPath == "" {
			target = base
		} else {
			target, detail.Err = resolveServiceTarget(service.BaseURL, requestedPath)
			if detail.Err != nil {
				return detail
			}
		}
	} else {
		var parseErr error
		target, parseErr = parseServiceURL(serviceName)
		if parseErr != nil {
			detail.Err = fmt.Errorf("unknown service %q; use a registered name or an absolute HTTP(S) URL", serviceName)
			return detail
		}
		if requestedPath != "" {
			detail.Err = fmt.Errorf("path must be omitted when service is an absolute URL")
			return detail
		}
		service = config.Service{BaseURL: serviceName, Auth: "none", TimeoutS: 60, MaxBodyKB: 64}
		requestedPath = serviceName
	}
	if err := addServiceQuery(target, args["query"]); err != nil {
		detail.Err = err
		return detail
	}
	c.mu.Lock()
	listener := c.listener
	c.mu.Unlock()
	if listener != "" && sameListenerTarget(target, listener) {
		detail.Err = fmt.Errorf("call_service refused the Agent_b listener %s", target.Host)
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

	authorization, token, operatorContext := "", "", false
	credentialHeaders := http.Header{}
	// Item 2nv (f): the origin the credential is approved for, resolved BEFORE any
	// credential is acquired. A connector with no credential is untouched by this.
	credentialOrigin := ""
	if registered {
		var err error
		authorization, token, credentialHeaders, operatorContext, err = c.authorization(ctx, serviceName, service, target)
		if err != nil {
			detail.Err = err
			return detail
		}
		if authorization != "" || len(credentialHeaders) > 0 {
			credentialOrigin = credential.OriginOf(target)
		}
	}
	detail.OperatorContext = operatorContext
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		detail.Err = fmt.Errorf("build service request: %w", err)
		return detail
	}
	request.Header = headers
	for name, values := range credentialHeaders {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}

	started := c.now()
	client := c.serviceClient(service, credentialHost, credentialOrigin)
	response, err := client.Do(request)
	duration := c.now().Sub(started).Milliseconds()
	if err != nil {
		detail.Err = fmt.Errorf("service request failed: %w", err)
		log.Printf("call_service: service=%q method=%s path=%q duration_ms=%d error=%q", serviceName, method, requestedPath, duration, detail.Err)
		return detail
	}
	defer response.Body.Close()
	contentType, rendered := renderedServiceType(response.Header.Get("Content-Type"))
	if !rendered {
		file, saveErr := c.saveServiceFile(response.Body, item, operationName, contentType, response.Header.Get("Content-Disposition"), service.MaxBodyKB, authorization, token, credentialHeaders)
		if saveErr != nil {
			detail.Err = saveErr
			return detail
		}
		encoded, _ := json.Marshal(file)
		detail.Content = string(encoded)
		detail.Metadata = map[string]any{"service": serviceName, "method": method, "path": requestedPath, "status": response.StatusCode, "duration_ms": duration, "file": file}
		log.Printf("call_service: service=%q method=%s path=%q status=%d duration_ms=%d file_bytes=%d", serviceName, method, requestedPath, response.StatusCode, duration, file["bytes"])
		return detail
	}

	window, err := readServiceWindow(response.Body, offset, limit)
	if err != nil {
		detail.Err = err
		return detail
	}
	cleanBody := redactServiceCredential(window.Content, authorization, token)
	for name, values := range credentialHeaders {
		for _, value := range values {
			cleanBody = strings.ReplaceAll(cleanBody, value, "[redacted]")
			if strings.EqualFold(name, "Authorization") {
				if _, secret, err := bearerAuthorization(value); err == nil {
					cleanBody = strings.ReplaceAll(cleanBody, secret, "[redacted]")
				}
			}
		}
	}
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

func renderedServiceType(value string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil || mediaType == "" {
		return strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), true
	}
	mediaType = strings.ToLower(mediaType)
	rendered := strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml") || mediaType == "application/json" || mediaType == "application/xml" || mediaType == "application/x-www-form-urlencoded"
	return mediaType, rendered
}

func (c *CallService) saveServiceFile(body io.Reader, item *session.Session, operation, contentType, disposition string, maxKB int, authorization, token string, headers http.Header) (map[string]any, error) {
	if item == nil || item.Workspace == "" {
		return nil, fmt.Errorf("file response needs a chat workspace; nothing was saved")
	}
	name := serviceResponseName(disposition, operation, contentType, c.now())
	limit := int64(maxKB) << 10
	content, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("save file response: %w", err)
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file response exceeds this service's max_body_kb (%d KB); nothing was saved", maxKB)
	}
	secrets := []string{authorization, token}
	for _, values := range headers {
		secrets = append(secrets, values...)
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(content, []byte(secret)) {
			return nil, fmt.Errorf("file response contained a credential and was not saved")
		}
	}
	temporary, err := os.CreateTemp(item.Workspace, ".agentb-service-*")
	if err != nil {
		return nil, fmt.Errorf("save file response: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	written, writeErr := temporary.Write(content)
	if writeErr == nil && written != len(content) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	if closeErr := temporary.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil || written != len(content) {
		return nil, fmt.Errorf("save file response: %v", writeErr)
	}
	path, err := unusedServicePath(item.Workspace, name)
	if err != nil {
		return nil, err
	}
	if err := os.Link(temporaryName, path); err != nil {
		return nil, fmt.Errorf("save file response: %w", err)
	}
	relative := filepath.ToSlash(filepath.Base(path))
	digest := sha256.Sum256(content)
	return map[string]any{"path": relative, "bytes": int64(written), "sha256": fmt.Sprintf("%x", digest), "content_type": contentType}, nil
}

func serviceResponseName(disposition, operation, contentType string, now time.Time) string {
	if _, params, err := mime.ParseMediaType(disposition); err == nil {
		if name, err := attachment.SanitizeName(params["filename"]); err == nil {
			return name
		}
	}
	stem, err := attachment.SanitizeName(strings.TrimSpace(operation))
	if err != nil {
		stem = "response"
	}
	extensions, _ := mime.ExtensionsByType(contentType)
	sort.Strings(extensions)
	extension := ".bin"
	if len(extensions) > 0 {
		extension = extensions[0]
	}
	return fmt.Sprintf("%s-%s%s", stem, now.UTC().Format("20060102-150405"), extension)
}

func unusedServicePath(root, name string) (string, error) {
	extension, stem := filepath.Ext(name), strings.TrimSuffix(name, filepath.Ext(name))
	for suffix := 1; suffix < 10000; suffix++ {
		candidate := name
		if suffix > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, suffix, extension)
		}
		path, err := Resolve(root, candidate)
		if err != nil {
			return "", err
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("save file response: no unused name for %s", name)
}

func (c *CallService) service(name string) (config.Service, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	service, ok := c.services[name]
	return service, ok
}

func (c *CallService) authorization(ctx context.Context, name string, service config.Service, target *url.URL) (authorization, token string, headers http.Header, operatorContext bool, err error) {
	auth := strings.TrimSpace(service.Auth)
	if auth == "none" {
		return "", "", nil, false, nil
	}
	// Item 2nv (b): a stored credential, by reference. Its binding carries the origin and
	// the header, and the origin is checked before the secret is read.
	if strings.HasPrefix(auth, "stored:") {
		attached, storedErr := c.storedCredential(auth, target)
		if storedErr != nil {
			return "", "", nil, false, storedErr
		}
		if strings.EqualFold(attached.name, "Authorization") {
			return attached.value, attached.secret, nil, false, nil
		}
		header := http.Header{}
		header.Set(attached.name, attached.value)
		return "", attached.secret, header, false, nil
	}
	if scheme, reference, ok := strings.Cut(auth, ":"); ok {
		c.mu.Lock()
		provider := c.providers[strings.ToLower(scheme)]
		c.mu.Unlock()
		if provider != nil {
			origin, originErr := approvedOrigin(service.BaseURL)
			if originErr != nil {
				return "", "", nil, false, fmt.Errorf("auth_error: this connector's address is not an https origin, so a credential cannot be bound to it")
			}
			if err := enforceDestination(origin, target); err != nil {
				return "", "", nil, false, err
			}
			bound, boundErr := provider.Origin(strings.TrimSpace(reference))
			if boundErr != nil {
				return "", "", nil, false, fmt.Errorf("auth_error: %w", boundErr)
			}
			if err := enforceDestination(bound, target); err != nil {
				return "", "", nil, false, err
			}
			value, tokenErr := provider.Token(ctx, strings.TrimSpace(reference))
			if tokenErr != nil {
				return "", "", nil, false, tokenErr
			}
			authorization, token, err = bearerAuthorization(value)
			return authorization, token, nil, false, err
		}
	}
	if strings.HasPrefix(auth, "static_bearer:") {
		value := strings.TrimSpace(os.Getenv(strings.TrimSpace(strings.TrimPrefix(auth, "static_bearer:"))))
		if value == "" {
			return "", "", nil, false, fmt.Errorf("auth_error: configured bearer environment variable is empty")
		}
		authorization, token, err = bearerAuthorization(value)
		return authorization, token, nil, false, err
	}
	if !strings.HasPrefix(auth, "exec:") {
		return "", "", nil, false, fmt.Errorf("auth_error: unsupported configured auth mode")
	}

	c.mu.Lock()
	if cached, ok := c.cache[name]; ok && c.now().Before(cached.validUntil) {
		c.mu.Unlock()
		return cached.authorization, cached.token, cached.headers.Clone(), true, nil
	}
	c.mu.Unlock()

	argv, err := splitServiceArgv(strings.TrimSpace(strings.TrimPrefix(auth, "exec:")))
	if err != nil || len(argv) == 0 {
		return "", "", nil, true, fmt.Errorf("auth_error: invalid credential argv")
	}
	credentialContext, cancel := context.WithTimeout(ctx, time.Duration(service.TimeoutS)*time.Second)
	defer cancel()
	// Item 2nv (f): a helper's output is a credential like any other, so the request's
	// destination must be the connector's own approved origin before the helper is run.
	origin, originErr := approvedOrigin(service.BaseURL)
	if originErr != nil {
		return "", "", nil, true, fmt.Errorf("auth_error: this connector's address is not an https origin, so a credential cannot be bound to it")
	}
	if destinationErr := enforceDestination(origin, target); destinationErr != nil {
		return "", "", nil, true, destinationErr
	}
	command := exec.CommandContext(credentialContext, argv[0], argv[1:]...)
	// (g): no window on the operator's desktop, ever. Measured at W0: this child had
	// none of the quiet-start flags, so a console helper flashed a window.
	quietproc.Quiet(command)
	output, runErr := command.Output()
	if credentialContext.Err() == context.DeadlineExceeded {
		return "", "", nil, true, fmt.Errorf("auth_error: credential command timed out")
	}
	if runErr != nil {
		return "", "", nil, true, fmt.Errorf("auth_error: credential command failed")
	}
	line := strings.TrimSpace(string(output))
	if line == "" || strings.ContainsAny(line, "\r\n") {
		return "", "", nil, true, fmt.Errorf("auth_error: credential output must be one non-empty line")
	}
	if strings.EqualFold(argv[len(argv)-1], "headers") {
		var values map[string]string
		if json.Unmarshal([]byte(line), &values) != nil || len(values) == 0 {
			return "", "", nil, true, fmt.Errorf("auth_error: credential headers JSON is invalid")
		}
		headers = http.Header{}
		for name, value := range values {
			if strings.EqualFold(name, "Host") || strings.TrimSpace(name) == "" || strings.ContainsAny(value, "\r\n") {
				return "", "", nil, true, fmt.Errorf("auth_error: credential header is invalid")
			}
			headers.Set(name, value)
		}
		c.mu.Lock()
		c.cache[name] = cachedServiceCredential{headers: headers.Clone(), validUntil: c.now().Add(5 * time.Minute)}
		c.mu.Unlock()
		return "", "", headers, true, nil
	}
	expires := time.Time{}
	if strings.HasPrefix(line, "{") {
		var value struct {
			Token     string `json:"token"`
			ExpiresAt string `json:"expires_at"`
		}
		if json.Unmarshal([]byte(line), &value) != nil || strings.TrimSpace(value.Token) == "" {
			return "", "", nil, true, fmt.Errorf("auth_error: credential JSON is invalid")
		}
		line = strings.TrimSpace(value.Token)
		if value.ExpiresAt != "" {
			expires, err = time.Parse(time.RFC3339, value.ExpiresAt)
			if err != nil {
				return "", "", nil, true, fmt.Errorf("auth_error: credential expires_at must be RFC3339")
			}
		}
	}
	authorization, token, err = bearerAuthorization(line)
	if err != nil {
		return "", "", nil, true, err
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
	return authorization, token, nil, true, nil
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
	base, err := parseServiceURL(baseURL)
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

func parseServiceURL(raw string) (*url.URL, error) {
	value, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || value == nil || !value.IsAbs() || value.Host == "" || (value.Scheme != "http" && value.Scheme != "https") || value.User != nil {
		return nil, fmt.Errorf("absolute HTTP(S) URL without embedded credentials is required")
	}
	return value, nil
}

func parseOptionalAbsoluteServiceURL(raw string) (*url.URL, bool, error) {
	value, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, false, fmt.Errorf("path is invalid")
	}
	if !value.IsAbs() {
		return nil, false, nil
	}
	value, err = parseServiceURL(raw)
	return value, true, err
}

func credentialHostMismatch(serviceName, registeredHost, requestedHost string) error {
	return fmt.Errorf("service %q credential host mismatch: registered host %q, requested host %q", serviceName, registeredHost, requestedHost)
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

// serviceClient is the request's client. Item 2nv (f): when a credential is attached, a
// redirect that leaves the approved ORIGIN — another scheme, another port, another host —
// is refused rather than followed, because the credential travels with the redirect.
// The host-only rule stays for a connector with no credential.
func (c *CallService) serviceClient(service config.Service, credentialHost, credentialOrigin string) *http.Client {
	c.mu.Lock()
	client := c.testClient
	c.mu.Unlock()
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	} else {
		copied := *client
		client = &copied
	}
	client.Timeout = time.Duration(service.TimeoutS) * time.Second
	client.CheckRedirect = nil
	if credentialOrigin != "" {
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if err := credential.AllowsOrigin(credentialOrigin, request.URL); err != nil {
				return fmt.Errorf("the redirect was not followed: %w", err)
			}
			return nil
		}
		return client
	}
	if credentialHost != "" {
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if !strings.EqualFold(request.URL.Host, credentialHost) {
				return credentialHostMismatch("redirect", credentialHost, request.URL.Host)
			}
			return nil
		}
	}
	return client
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

// --- item 2nr: the imported document, the operations it offers, and the call ---

// enabledOperations reads the connector's snapshot, checks it against the hash recorded
// when the operator approved it, and returns the operations he enabled. (g): a snapshot
// that no longer matches refuses the connector's operations with one line, because a
// document changed on disk is a document nobody approved.
func (c *CallService) enabledOperations(name string, service config.Service) ([]ServiceOperation, error) {
	imported := service.OpenAPI
	if imported == nil {
		return nil, fmt.Errorf("service %q has no imported document", name)
	}
	raw, err := os.ReadFile(imported.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("service %q: its approved document is unreadable, so its operations are refused", name)
	}
	if digest := DocumentDigest(raw); !strings.EqualFold(digest, imported.SHA256) {
		return nil, fmt.Errorf("service %q: its approved document has changed since it was approved, so its operations are refused; import it again", name)
	}
	document, err := ParseServiceDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("service %q: %w", name, err)
	}
	return document.Enabled(imported.Operations)
}

// operationArgs turns {service, operation, params} into the ordinary {service, method,
// path, query, body} call, and refuses everything the document does not offer.
func (c *CallService) operationArgs(name string, service config.Service, args map[string]any) (map[string]any, error) {
	operations, err := c.enabledOperations(name, service)
	if err != nil {
		return nil, err
	}
	offered := []string{}
	for _, operation := range operations {
		offered = append(offered, operation.ID)
	}
	wanted, _ := args["operation"].(string)
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return nil, fmt.Errorf("service %q answers only its imported operations: %s", name, strings.Join(offered, ", "))
	}
	var chosen *ServiceOperation
	for index := range operations {
		if operations[index].ID == wanted {
			chosen = &operations[index]
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("service %q offers no operation %q; it offers %s", name, wanted, strings.Join(offered, ", "))
	}
	params := map[string]any{}
	if raw, present := args["params"]; present && raw != nil {
		values, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("params must be an object")
		}
		params = values
	}
	path, query, body, err := chosen.Fill(params)
	if err != nil {
		return nil, err
	}
	rewritten := map[string]any{"service": name, "method": chosen.Method, "path": strings.TrimPrefix(path, "/")}
	if len(query) > 0 {
		values := map[string]any{}
		for key := range query {
			values[key] = query.Get(key)
		}
		rewritten["query"] = values
	}
	if len(body) > 0 {
		rewritten["body"] = body
	}
	for _, carried := range []string{"offset", "limit", "headers"} {
		if value, present := args[carried]; present {
			rewritten[carried] = value
		}
	}
	accept, _ := args["accept"].(string)
	accept = strings.ToLower(strings.TrimSpace(accept))
	if accept != "" {
		if !containsString(chosen.ResponseTypes, accept) {
			return nil, fmt.Errorf("operation %s does not declare %s; it declares %s", chosen.ID, accept, strings.Join(chosen.ResponseTypes, ", "))
		}
		headers, _ := rewritten["headers"].(map[string]any)
		if headers == nil {
			headers = map[string]any{}
		}
		headers["Accept"] = accept
		rewritten["headers"] = headers
	}
	return rewritten, nil
}

// operationsDescription is what the model reads in the tool's own definition.
func (c *CallService) operationsDescription() string {
	c.mu.Lock()
	names := make([]string, 0, len(c.services))
	services := make(map[string]config.Service, len(c.services))
	for name, service := range c.services {
		if service.OpenAPI != nil {
			names = append(names, name)
			services[name] = service
		}
	}
	c.mu.Unlock()
	if len(names) == 0 {
		return "Operation name, for a service that has imported an OpenAPI document"
	}
	sort.Strings(names)
	lines := []string{"Operation name for a service that has imported an OpenAPI document. Such a service answers ONLY these operations, called with params rather than method and path:"}
	for _, name := range names {
		operations, err := c.enabledOperations(name, services[name])
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: unavailable — %v", name, err))
			continue
		}
		for _, operation := range operations {
			lines = append(lines, name+"."+operation.Line())
		}
	}
	return strings.Join(lines, "\n")
}
