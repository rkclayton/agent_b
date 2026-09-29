package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Item 2nr: an OpenAPI document, read once, becomes the operations a connector offers.
//
// What this is for: `call_service` already carries registered connectors — a host, a
// credential and a set of HTTP methods — but it knows nothing about what lives on that
// host, so the model has to be told method, path and parameters in prose and can call any
// path the method allows. A document turns that into named operations with checked
// parameters, and the document becomes the allow-list.
//
// What this is NOT: a general OpenAPI implementation. It reads the JSON 3.0/3.1 shapes
// the acceptance names — operations, parameters in path and query, a JSON request body,
// local $refs into components — and refuses everything else with one line rather than
// guessing. No dependency: the standard library parses it, and YAML is refused, not
// converted.

// ServiceOperationParam is one parameter of an operation, as the document declares it.
type ServiceOperationParam struct {
	Name        string
	In          string // path, query or body
	Type        string // string, integer, number or boolean
	Description string
	Required    bool
	Enum        []string
}

// ServiceOperation is one callable operation.
type ServiceOperation struct {
	ID      string
	Method  string
	Path    string
	Summary string
	Params  []ServiceOperationParam
}

// ServiceDocument is what an imported document says.
type ServiceDocument struct {
	Version    string
	Auth       string // one line: what the document says its OWN auth wants, shown and never used
	Operations []ServiceOperation
}

// DocumentDigest is the hash a snapshot is pinned by.
func DocumentDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// methodOrder is the order operations are listed in. A JSON object has no order once it
// is decoded, so the listing is sorted — by path, then by this sequence — rather than
// pretending to follow the document.
var methodOrder = []string{"get", "post", "put", "patch", "delete", "head", "options"}

type rawSchema struct {
	Type       any                  `json:"type"`
	Enum       []any                `json:"enum"`
	Properties map[string]rawSchema `json:"properties"`
	Required   []string             `json:"required"`
	Ref        string               `json:"$ref"`
}

type rawParameter struct {
	Ref         string    `json:"$ref"`
	Name        string    `json:"name"`
	In          string    `json:"in"`
	Required    bool      `json:"required"`
	Description string    `json:"description"`
	Schema      rawSchema `json:"schema"`
}

type rawOperation struct {
	OperationID string         `json:"operationId"`
	Summary     string         `json:"summary"`
	Parameters  []rawParameter `json:"parameters"`
	RequestBody *struct {
		Required bool `json:"required"`
		Content  map[string]struct {
			Schema rawSchema `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

type rawDocument struct {
	OpenAPI    string                             `json:"openapi"`
	Paths      map[string]map[string]rawOperation `json:"paths"`
	Components struct {
		Parameters      map[string]rawParameter `json:"parameters"`
		SecuritySchemes map[string]struct {
			Type   string `json:"type"`
			In     string `json:"in"`
			Name   string `json:"name"`
			Scheme string `json:"scheme"`
		} `json:"securitySchemes"`
	} `json:"components"`
}

// ParseServiceDocument reads an OpenAPI 3.0 or 3.1 JSON document.
func ParseServiceDocument(raw []byte) (ServiceDocument, error) {
	var document rawDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return ServiceDocument{}, fmt.Errorf("the document is not OpenAPI 3.0 or 3.1 JSON (YAML is not read): %w", err)
	}
	if !strings.HasPrefix(document.OpenAPI, "3.0") && !strings.HasPrefix(document.OpenAPI, "3.1") {
		return ServiceDocument{}, fmt.Errorf("the document declares openapi %q; only 3.0 and 3.1 JSON are read", document.OpenAPI)
	}
	parsed := ServiceDocument{Version: document.OpenAPI, Auth: documentAuth(document)}
	paths := make([]string, 0, len(document.Paths))
	for path := range document.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for _, method := range methodOrder {
			operation, present := document.Paths[path][method]
			if !present {
				continue
			}
			id := strings.TrimSpace(operation.OperationID)
			if id == "" {
				continue
			}
			built, err := buildOperation(document, id, method, path, operation)
			if err != nil {
				return ServiceDocument{}, err
			}
			parsed.Operations = append(parsed.Operations, built)
		}
	}
	if len(parsed.Operations) == 0 {
		return ServiceDocument{}, fmt.Errorf("the document declares no operation with an operationId")
	}
	return parsed, nil
}

func documentAuth(document rawDocument) string {
	names := make([]string, 0, len(document.Components.SecuritySchemes))
	for name := range document.Components.SecuritySchemes {
		names = append(names, name)
	}
	sort.Strings(names)
	said := []string{}
	for _, name := range names {
		scheme := document.Components.SecuritySchemes[name]
		switch strings.ToLower(scheme.Type) {
		case "apikey":
			said = append(said, fmt.Sprintf("%s: an API key in the %s named %s", name, strings.ToLower(scheme.In), scheme.Name))
		case "http":
			said = append(said, fmt.Sprintf("%s: an HTTP %s credential", name, strings.ToLower(scheme.Scheme)))
		default:
			said = append(said, fmt.Sprintf("%s: %s", name, scheme.Type))
		}
	}
	if len(said) == 0 {
		return "the document declares no authentication"
	}
	return strings.Join(said, "; ")
}

func buildOperation(document rawDocument, id, method, path string, operation rawOperation) (ServiceOperation, error) {
	built := ServiceOperation{ID: id, Method: strings.ToUpper(method), Path: path, Summary: strings.TrimSpace(operation.Summary)}
	for _, parameter := range operation.Parameters {
		if parameter.Ref != "" {
			resolved, err := resolveParameter(document, parameter.Ref)
			if err != nil {
				return ServiceOperation{}, err
			}
			parameter = resolved
		}
		place := strings.ToLower(parameter.In)
		if place != "path" && place != "query" {
			// header and cookie parameters are the connector's business, never the
			// model's: (f) keeps the credential where it is.
			continue
		}
		built.Params = append(built.Params, ServiceOperationParam{
			Name: parameter.Name, In: place, Type: schemaType(parameter.Schema), Description: strings.TrimSpace(parameter.Description),
			Required: parameter.Required || place == "path", Enum: schemaEnum(parameter.Schema),
		})
	}
	if operation.RequestBody != nil {
		content, present := operation.RequestBody.Content["application/json"]
		if !present {
			return ServiceOperation{}, fmt.Errorf("operation %s takes a body that is not application/json", id)
		}
		required := map[string]bool{}
		for _, name := range content.Schema.Required {
			required[name] = true
		}
		names := make([]string, 0, len(content.Schema.Properties))
		for name := range content.Schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			property := content.Schema.Properties[name]
			built.Params = append(built.Params, ServiceOperationParam{
				Name: name, In: "body", Type: schemaType(property), Required: required[name], Enum: schemaEnum(property),
			})
		}
	}
	return built, nil
}

func resolveParameter(document rawDocument, ref string) (rawParameter, error) {
	const prefix = "#/components/parameters/"
	if !strings.HasPrefix(ref, prefix) {
		return rawParameter{}, fmt.Errorf("the document uses the reference %q; only local #/components/parameters/… references are read", ref)
	}
	parameter, present := document.Components.Parameters[strings.TrimPrefix(ref, prefix)]
	if !present {
		return rawParameter{}, fmt.Errorf("the document references %q, which it does not define", ref)
	}
	return parameter, nil
}

// schemaType takes the basic type. OpenAPI 3.1 allows a list, usually to add "null";
// the first type that is not null is the one a parameter is checked against.
func schemaType(schema rawSchema) string {
	switch value := schema.Type.(type) {
	case string:
		return value
	case []any:
		for _, entry := range value {
			if text, ok := entry.(string); ok && text != "null" {
				return text
			}
		}
	}
	return "string"
}

func schemaEnum(schema rawSchema) []string {
	values := []string{}
	for _, entry := range schema.Enum {
		values = append(values, fmt.Sprint(entry))
	}
	return values
}

// Enabled returns the named operations, in the document's listed order, and refuses a
// name the document does not carry.
func (d ServiceDocument) Enabled(names []string) ([]ServiceOperation, error) {
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	enabled := []ServiceOperation{}
	for _, operation := range d.Operations {
		if wanted[operation.ID] {
			enabled = append(enabled, operation)
			delete(wanted, operation.ID)
		}
	}
	if len(wanted) > 0 {
		missing := make([]string, 0, len(wanted))
		for name := range wanted {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("the document declares no operation named %s", strings.Join(missing, ", "))
	}
	return enabled, nil
}

// Fill checks params against the document and returns the path, query and body of the
// request. NOTHING IS SENT until this returns: (e) is the whole point of the method.
func (o ServiceOperation) Fill(params map[string]any) (string, url.Values, map[string]any, error) {
	declared := map[string]ServiceOperationParam{}
	for _, parameter := range o.Params {
		declared[parameter.Name] = parameter
	}
	for name := range params {
		if _, present := declared[name]; !present {
			return "", nil, nil, fmt.Errorf("operation %s takes no parameter %q; it takes %s", o.ID, name, o.parameterNames())
		}
	}
	path := o.Path
	query := url.Values{}
	body := map[string]any{}
	for _, parameter := range o.Params {
		value, present := params[parameter.Name]
		if !present || value == nil {
			if parameter.Required {
				return "", nil, nil, fmt.Errorf("operation %s requires the parameter %q (%s, in %s)", o.ID, parameter.Name, parameter.Type, parameter.In)
			}
			continue
		}
		text, err := checkedParam(o.ID, parameter, value)
		if err != nil {
			return "", nil, nil, err
		}
		switch parameter.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+parameter.Name+"}", url.PathEscape(text))
		case "query":
			query.Set(parameter.Name, text)
		case "body":
			body[parameter.Name] = value
		}
	}
	if strings.Contains(path, "{") {
		return "", nil, nil, fmt.Errorf("operation %s leaves %s unfilled", o.ID, path)
	}
	return path, query, body, nil
}

// checkedParam refuses a wrong basic type or a value outside an enum, naming the
// parameter and the valid set, and returns the value as the request will carry it.
func checkedParam(id string, parameter ServiceOperationParam, value any) (string, error) {
	text := ""
	switch parameter.Type {
	case "integer", "number":
		number, ok := value.(float64)
		if !ok {
			if whole, isInt := value.(int); isInt {
				number, ok = float64(whole), true
			}
		}
		if !ok {
			return "", fmt.Errorf("operation %s wants %s for the parameter %q, and got %T", id, parameter.Type, parameter.Name, value)
		}
		if parameter.Type == "integer" && number != float64(int64(number)) {
			return "", fmt.Errorf("operation %s wants an integer for the parameter %q", id, parameter.Name)
		}
		text = strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", number), "0"), ".")
	case "boolean":
		flag, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("operation %s wants a boolean for the parameter %q, and got %T", id, parameter.Name, value)
		}
		text = fmt.Sprint(flag)
	default:
		word, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("operation %s wants a string for the parameter %q, and got %T", id, parameter.Name, value)
		}
		text = word
	}
	if len(parameter.Enum) > 0 {
		for _, allowed := range parameter.Enum {
			if allowed == text {
				return text, nil
			}
		}
		return "", fmt.Errorf("operation %s takes %s for the parameter %q", id, strings.Join(parameter.Enum, ", "), parameter.Name)
	}
	return text, nil
}

func (o ServiceOperation) parameterNames() string {
	names := []string{}
	for _, parameter := range o.Params {
		name := parameter.Name
		if parameter.Required {
			name += " (required)"
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "no parameters"
	}
	return strings.Join(names, ", ")
}

// Line is how one operation is written for the model and for the approval card.
func (o ServiceOperation) Line() string {
	line := fmt.Sprintf("%s — %s %s", o.ID, o.Method, o.Path)
	if o.Summary != "" {
		line += " — " + o.Summary
	}
	if len(o.Params) == 0 {
		return line
	}
	parts := []string{}
	for _, parameter := range o.Params {
		part := fmt.Sprintf("%s %s in %s", parameter.Name, parameter.Type, parameter.In)
		if parameter.Required {
			part += ", required"
		}
		if len(parameter.Enum) > 0 {
			part += " (" + strings.Join(parameter.Enum, ", ") + ")"
		}
		if parameter.Description != "" {
			part += " — " + parameter.Description
		}
		parts = append(parts, part)
	}
	return line + " — params: " + strings.Join(parts, "; ")
}

// --- the one fetch (b): the card and the approval share it ---

type fetchedDocument struct {
	raw        []byte
	validUntil time.Time
}

var (
	documentMu    sync.Mutex
	documentCache = map[string]fetchedDocument{}
)

// LoadServiceDocument returns the document a proposal names: an https URL, fetched once,
// or a file the operator supplied. The result is held for five minutes so that showing
// the operations on the approval card and recording them on approval are ONE fetch, as
// (b) says. Nothing is written here; the snapshot is written when the operator approves.
func LoadServiceDocument(ctx context.Context, source string) ([]byte, ServiceDocument, error) {
	source = strings.TrimSpace(source)
	documentMu.Lock()
	cached, present := documentCache[source]
	documentMu.Unlock()
	raw := cached.raw
	if !present || time.Now().After(cached.validUntil) {
		fetched, err := readServiceDocument(ctx, source)
		if err != nil {
			return nil, ServiceDocument{}, err
		}
		raw = fetched
		documentMu.Lock()
		documentCache[source] = fetchedDocument{raw: raw, validUntil: time.Now().Add(5 * time.Minute)}
		documentMu.Unlock()
	}
	document, err := ParseServiceDocument(raw)
	if err != nil {
		return nil, ServiceDocument{}, err
	}
	return raw, document, nil
}

func readServiceDocument(ctx context.Context, source string) ([]byte, error) {
	lowered := strings.ToLower(source)
	if strings.HasPrefix(lowered, "http://") && !loopbackDocument(source) {
		return nil, fmt.Errorf("an OpenAPI document must be fetched over https, or given as a file")
	}
	if strings.HasPrefix(lowered, "https://") || strings.HasPrefix(lowered, "http://") {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("the document address is invalid")
		}
		request.Header.Set("Accept", "application/json")
		response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
		if err != nil {
			return nil, fmt.Errorf("the document could not be fetched")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the document answered HTTP %d", response.StatusCode)
		}
		return io.ReadAll(io.LimitReader(response.Body, 4<<20))
	}
	if !filepath.IsAbs(source) {
		return nil, fmt.Errorf("a document file must be given by its full path")
	}
	return os.ReadFile(source)
}

// loopbackDocument allows plain http for a document on THIS machine and nowhere else: a
// service the operator is running locally is not a network hop, and refusing it would
// mean the acceptance could only be run against something remote.
func loopbackDocument(source string) bool {
	parsed, err := url.Parse(source)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}
