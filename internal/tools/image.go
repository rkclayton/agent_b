package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/attachment"
	"harness/internal/config"
	"harness/internal/llm"
	"harness/internal/session"
)

type Image struct {
	mu  sync.RWMutex
	cfg config.Config
}

func NewImage() *Image      { return &Image{} }
func (*Image) Name() string { return "image" }
func (*Image) Description() string {
	return "Make pictures from a description, or change workspace pictures from a description. Returned pictures are saved in the chat folder."
}
func (*Image) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"description": map[string]any{"type": "string"},
		"pictures":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 16},
		"size":        map[string]any{"type": "string"},
		"count":       map[string]any{"type": "integer", "minimum": 1, "maximum": 4, "default": 1},
	}, "required": []string{"description"}}
}
func (i *Image) Configure(cfg config.Config) { i.mu.Lock(); i.cfg = cfg; i.mu.Unlock() }
func (i *Image) config() config.Config       { i.mu.RLock(); defer i.mu.RUnlock(); return i.cfg }

func (i *Image) Call(ctx context.Context, item *session.Session, args map[string]any) (string, error) {
	description, _ := args["description"].(string)
	description = strings.TrimSpace(description)
	if description == "" {
		return "", fmt.Errorf("description is required")
	}
	count := 1
	if value, ok := args["count"].(float64); ok {
		count = int(value)
	}
	if count < 1 || count > 4 {
		return "", fmt.Errorf("count must be from one to four")
	}
	cfg := i.config()
	connectionID := strings.TrimSpace(cfg.Images.Connection)
	explicit := connectionID != ""
	if !explicit {
		connectionID = item.ConnectionID
	}
	connection := imageConnection(cfg.Connections, connectionID)
	if connection == nil || (!explicit && !connection.Capabilities.Images) {
		return "", fmt.Errorf("making pictures is not set up")
	}
	input, err := imageInputs(ctx, item, args["pictures"])
	if err != nil {
		return "", err
	}
	if !item.ReserveGeneratedImages(count) {
		return "", fmt.Errorf("picture limit is eight per run")
	}
	results, err := llm.New(connection).Images(ctx, llm.ImageRequest{Prompt: description, Model: strings.TrimSpace(cfg.Images.Model), Size: strings.TrimSpace(stringArg(args, "size")), Count: count, Images: input})
	if err != nil {
		item.ReleaseGeneratedImages(count)
		return "", imageCallError(err)
	}
	if len(results) > count {
		item.ReleaseGeneratedImages(count)
		return "", fmt.Errorf("server returned more pictures than requested")
	}
	if len(results) < count {
		item.ReleaseGeneratedImages(count - len(results))
	}
	paths := make([]string, 0, len(results))
	for index, result := range results {
		ext, valid := imageExtension(result.Data, result.Format)
		if !valid {
			return "", fmt.Errorf("server returned a file that is not a picture")
		}
		name := fmt.Sprintf("image-%d-%d.%s", time.Now().UTC().UnixNano(), index+1, ext)
		root, rootErr := item.WriteRoot(name)
		if rootErr != nil {
			return "", rootErr
		}
		path, resolveErr := Resolve(root, name)
		if resolveErr != nil {
			return "", resolveErr
		}
		if writeErr := os.WriteFile(path, result.Data, 0o600); writeErr != nil {
			return "", writeErr
		}
		paths = append(paths, filepath.ToSlash(name))
	}
	return "saved pictures: " + strings.Join(paths, ", "), nil
}

func (i *Image) CallDetailed(ctx context.Context, item *session.Session, args map[string]any) CallDetail {
	content, err := i.Call(ctx, item, args)
	if err != nil {
		return CallDetail{Err: err}
	}
	names := strings.Split(strings.TrimPrefix(content, "saved pictures: "), ", ")
	files := make([]any, 0, len(names))
	for _, name := range names {
		root, rootErr := item.WriteRoot(name)
		if rootErr != nil {
			return CallDetail{Err: rootErr}
		}
		path, resolveErr := Resolve(root, name)
		if resolveErr != nil {
			return CallDetail{Err: resolveErr}
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return CallDetail{Err: statErr}
		}
		files = append(files, map[string]any{"path": name, "bytes": info.Size()})
	}
	return CallDetail{Content: content, Metadata: map[string]any{"files": files}}
}

func imageConnection(connections []config.Connection, id string) *config.Connection {
	for index := range connections {
		if connections[index].ID == id {
			value := connections[index]
			return &value
		}
	}
	return nil
}

func imageInputs(ctx context.Context, item *session.Session, raw any) ([]llm.ImageInput, error) {
	values, ok := raw.([]any)
	if raw == nil {
		return nil, nil
	}
	if !ok {
		return nil, fmt.Errorf("pictures must be a list of files")
	}
	result := make([]llm.ImageInput, 0, len(values))
	for _, value := range values {
		name, ok := value.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("each picture must name a file")
		}
		if attachment.Classify(name) != attachment.Image {
			return nil, fmt.Errorf("picture is not an image file: %s", name)
		}
		root, err := item.ReadRoot(name)
		if err != nil {
			return nil, err
		}
		path, err := resolveForSessionTool(ctx, item, root, name)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) > 20<<20 {
			return nil, fmt.Errorf("picture exceeds 20 MiB: %s", name)
		}
		result = append(result, llm.ImageInput{Name: filepath.Base(name), Data: data})
	}
	return result, nil
}

func imageCallError(err error) error {
	var shape *llm.ImageHTTPError
	if errors.As(err, &shape) {
		if shape.Status == http.StatusUnauthorized || shape.Status == http.StatusForbidden {
			return fmt.Errorf("the server refused the key")
		}
		if shape.Status == http.StatusNotFound || shape.Status == http.StatusMethodNotAllowed {
			return fmt.Errorf("the server does not serve pictures")
		}
		if shape.Status >= 400 && shape.Status < 500 {
			return fmt.Errorf("the server refused the description: %s", imageOneLine(shape.Message))
		}
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return fmt.Errorf("the picture request timed out")
	}
	return fmt.Errorf("picture request failed: %s", imageOneLine(err.Error()))
}

func imageExtension(data []byte, _ string) (string, bool) {
	kind := http.DetectContentType(data)
	switch kind {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}
func imageOneLine(value string) string { return strings.Join(strings.Fields(value), " ") }
