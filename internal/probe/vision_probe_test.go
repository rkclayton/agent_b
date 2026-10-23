package probe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"harness/internal/config"
	"harness/internal/llm"
)

func TestProbeVisionClassifiesAllResponseShapes(t *testing.T) {
	for _, shape := range []string{config.VisionReadsImages, config.VisionAcceptsUnreadable, config.VisionRejected} {
		t.Run(shape, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if shape == config.VisionRejected {
					http.Error(w, "images rejected", http.StatusBadRequest)
					return
				}
				var body struct {
					Messages []struct {
						Content []map[string]any `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				imageURL := body.Messages[0].Content[1]["image_url"].(map[string]any)["url"].(string)
				encoded := strings.TrimPrefix(imageURL, "data:image/png;base64,")
				imageBytes, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					t.Fatal(err)
				}
				digit := ""
				for candidate := 0; candidate < 10; candidate++ {
					if bytes.Equal(imageBytes, probeDigitPNG(candidate)) {
						digit = string(rune('0' + candidate))
					}
				}
				if digit == "" {
					t.Fatal("probe image did not match a generated digit")
				}
				if shape == config.VisionAcceptsUnreadable {
					digit = "I cannot see an image"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": digit}}}})
			}))
			defer server.Close()

			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL = server.URL
			connection.Model = "vision-fixture"
			vision, accepted, detail := probeVision(context.Background(), llm.New(&connection))
			if vision != shape {
				t.Fatalf("vision=%q want=%q", vision, shape)
			}
			// 2wb: the finding names what was sent and what came back.
			if want := map[bool]string{true: "got \"", false: "HTTP "}[accepted]; !strings.HasPrefix(detail, "sent digit ") || !strings.Contains(detail, want) {
				t.Fatalf("detail=%q want %q", detail, want)
			}
			if accepted != (shape != config.VisionRejected) {
				t.Fatalf("accepted=%t shape=%q", accepted, shape)
			}
		})
	}
}

func TestProbeImagesDistinguishesAValidationResponseFromMissingRoute2s3(t *testing.T) {
	for _, row := range []struct {
		name   string
		status int
		want   bool
	}{{"served", http.StatusBadRequest, true}, {"missing", http.StatusNotFound, false}} {
		t.Run(row.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests++
				if request.URL.Path != "/v1/images/generations" {
					t.Fatalf("path=%s", request.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len(body) != 0 {
					t.Fatalf("probe could make a picture: %v", body)
				}
				http.Error(w, "validation", row.status)
			}))
			defer server.Close()
			connection := config.Defaults(t.TempDir()).Connections[0]
			connection.BaseURL = server.URL
			if got := probeImages(context.Background(), llm.New(&connection)); got != row.want {
				t.Fatalf("images=%t want=%t", got, row.want)
			}
			if requests != 1 {
				t.Fatalf("requests=%d", requests)
			}
		})
	}
}

func TestLiveVisionProbe(t *testing.T) {
	baseURL := os.Getenv("AGENTB_VISION_LIVE_URL")
	if baseURL == "" {
		t.Skip("set AGENTB_VISION_LIVE_URL and AGENTB_VISION_LIVE_MODEL for an authorized live classification")
	}
	connection := config.Defaults(t.TempDir()).Connections[0]
	connection.BaseURL = baseURL
	connection.Model = os.Getenv("AGENTB_VISION_LIVE_MODEL")
	vision, accepted, detail := probeVision(context.Background(), llm.New(&connection))
	t.Logf("LIVE_VISION_CLASSIFICATION url=%s model=%s vision=%q accepted=%t detail=%s", baseURL, connection.Model, vision, accepted, detail)
}
