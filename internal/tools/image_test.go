package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

var imageFixturePNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 'I', 'E', 'N', 'D'}

func imageToolFixture(t *testing.T, handler http.Handler) (*Image, *session.Session, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	cfg := config.Defaults(t.TempDir())
	cfg.Connections[0].ID, cfg.Connections[0].BaseURL, cfg.Connections[0].APIKey = "chat", server.URL, "secret"
	cfg.Connections[0].RequestTimeoutS = 1
	cfg.Connections[0].Capabilities.Images = true
	tool := NewImage()
	tool.Configure(cfg)
	item := &session.Session{ID: "chat", ConnectionID: "chat", Workspace: t.TempDir(), ToolCalls: map[string]int{}, ToolsEnabled: map[string]bool{"image": true}}
	item.ResetRunTouches()
	return tool, item, server
}

func TestImageMakesInlineAndAddressPicturesWithoutForwardingTheKey2s3(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	asset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("key reached returned-address host")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageFixturePNG)
	}))
	defer asset.Close()
	tool, item, server := imageToolFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["prompt"] != "draw a dot" || body["size"] != "64x64" || body["n"] != float64(1) {
			t.Fatalf("body=%v", body)
		}
		mu.Lock()
		requests++
		call := requests
		mu.Unlock()
		value := map[string]any{"b64_json": base64.StdEncoding.EncodeToString(imageFixturePNG)}
		if call == 2 {
			value = map[string]any{"url": asset.URL + "/result.png"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{value}, "output_format": "png"})
	}))
	defer server.Close()
	for index := 0; index < 2; index++ {
		got, err := tool.Call(context.Background(), item, map[string]any{"description": "draw a dot", "size": "64x64", "count": float64(1)})
		if err != nil || !strings.Contains(got, "image-") || !strings.Contains(got, ".png") {
			t.Fatalf("result=%q err=%v", got, err)
		}
	}
	files, err := filepath.Glob(filepath.Join(item.Workspace, "image-*.png"))
	if err != nil || len(files) != 2 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	for _, file := range files {
		if data, _ := os.ReadFile(file); !strings.HasPrefix(string(data), string(imageFixturePNG[:8])) {
			t.Fatalf("not png: %s", file)
		}
	}
}

func TestImageEditsWithMultipartPicturesAndModelChoice2s3(t *testing.T) {
	var receivedModel string
	tool, item, server := imageToolFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/edits" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(2 << 20); err != nil {
			t.Fatal(err)
		}
		receivedModel = r.FormValue("model")
		if r.FormValue("prompt") != "make it blue" {
			t.Fatalf("prompt=%q", r.FormValue("prompt"))
		}
		files := r.MultipartForm.File["image[]"]
		if len(files) != 1 {
			t.Fatalf("files=%d", len(files))
		}
		part, _ := files[0].Open()
		data, _ := io.ReadAll(part)
		_ = part.Close()
		if string(data) != string(imageFixturePNG) {
			t.Fatal("edited input differs")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": base64.StdEncoding.EncodeToString(imageFixturePNG)}}, "output_format": "png"})
	}))
	defer server.Close()
	input := filepath.Join(item.Workspace, "input.png")
	if err := os.WriteFile(input, imageFixturePNG, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := tool.config()
	cfg.Images.Model = "picked-model"
	tool.Configure(cfg)
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "make it blue", "pictures": []any{"input.png"}}); err != nil {
		t.Fatal(err)
	}
	if receivedModel != "picked-model" {
		t.Fatalf("model=%q", receivedModel)
	}
	cfg.Images.Model = ""
	tool.Configure(cfg)
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "make it blue", "pictures": []any{"input.png"}}); err != nil {
		t.Fatal(err)
	}
	if receivedModel != "" {
		t.Fatalf("default model sent=%q", receivedModel)
	}
}

func TestImageChoosesConfiguredConnectionAndEnforcesRunLimit2s3(t *testing.T) {
	called := make(chan string, 4)
	server := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called <- name
			var body struct {
				Count int `json:"n"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			data := make([]any, max(1, body.Count))
			for index := range data {
				data[index] = map[string]any{"b64_json": base64.StdEncoding.EncodeToString(imageFixturePNG)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "output_format": "png"})
		}))
	}
	chat, picked := server("chat"), server("picked")
	defer chat.Close()
	defer picked.Close()
	cfg := config.Defaults(t.TempDir())
	cfg.Connections = []config.Connection{{ID: "chat", BaseURL: chat.URL, RequestTimeoutS: 1, Capabilities: config.Capabilities{Images: true}}, {ID: "picked", BaseURL: picked.URL, RequestTimeoutS: 1}}
	tool := NewImage()
	tool.Configure(cfg)
	item := &session.Session{ID: "s", ConnectionID: "chat", Workspace: t.TempDir(), ToolsEnabled: map[string]bool{"image": true}}
	item.ResetRunTouches()
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "one", "count": float64(4)}); err != nil {
		t.Fatal(err)
	}
	if got := <-called; got != "chat" {
		t.Fatalf("empty setting used %s", got)
	}
	cfg.Images.Connection = "picked"
	tool.Configure(cfg)
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "two", "count": float64(4)}); err != nil {
		t.Fatal(err)
	}
	if got := <-called; got != "picked" {
		t.Fatalf("setting used %s", got)
	}
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "ninth"}); err == nil || err.Error() != "picture limit is eight per run" {
		t.Fatalf("ninth err=%v", err)
	}
	select {
	case got := <-called:
		t.Fatalf("refused ninth picture reached %s", got)
	default:
	}
}

func TestImageErrorsAreOneLineAndLeaveNoFile2s3(t *testing.T) {
	cases := []struct {
		name    string
		handler http.Handler
		want    string
	}{
		{"key", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
		}), "the server refused the key"},
		{"description", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":{"message":"description denied"}}`, http.StatusBadRequest)
		}), "the server refused the description: description denied"},
		{"timeout", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(1200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}), "the picture request timed out"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool, item, server := imageToolFixture(t, test.handler)
			defer server.Close()
			_, err := tool.Call(context.Background(), item, map[string]any{"description": "x"})
			if err == nil || err.Error() != test.want || strings.Contains(err.Error(), "\n") {
				t.Fatalf("err=%v", err)
			}
			if files, _ := filepath.Glob(filepath.Join(item.Workspace, "image-*")); len(files) != 0 {
				t.Fatalf("files=%v", files)
			}
		})
	}
	tool, item, server := imageToolFixture(t, http.NotFoundHandler())
	defer server.Close()
	cfg := tool.config()
	cfg.Connections[0].Capabilities.Images = false
	tool.Configure(cfg)
	if _, err := tool.Call(context.Background(), item, map[string]any{"description": "x"}); err == nil || err.Error() != "making pictures is not set up" {
		t.Fatalf("unsupported err=%v", err)
	}
}
