package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/events"
)

func TestSpeechStreamFakeHelperScenarios(t *testing.T) {
	if os.Getenv("AGENTB_SPEECH_HELPER") != "" {
		switch os.Getenv("AGENTB_SPEECH_HELPER") {
		case "exit":
			os.Exit(7)
		case "device":
			fmt.Println(`{"error":"no input device","done":true}`)
		case "partials":
			fmt.Println(`{"stage":"device","device":"fixture microphone"}`)
			fmt.Println(`{"partial":"hello"}`)
			fmt.Println(`{"partial":"hello world"}`)
			fmt.Println(`{"final":"hello world"}`)
			fmt.Println(`{"done":true,"reason":"silence"}`)
		}
		os.Exit(0)
	}

	for _, test := range []struct{ name, want string }{
		{"exit", `"error":"helper exited (7)"`},
		{"device", `"error":"no input device"`},
		{"partials", `"final":"hello world"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "scripts", "speech-helper.ps1"), []byte("fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := config.Defaults(root)
			bus := events.NewBus()
			var journal []events.Event
			bus.SetSink(func(event events.Event) error { journal = append(journal, event); return nil })
			server := New(&cfg, filepath.Join(root, "harness.json"), root, RuntimeRoots{Application: root, Data: root}, bus)
			server.speech.status = &SpeechStatus{Available: true, Offline: true}
			server.speech.checked = time.Now()
			server.speechCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestSpeechStreamFakeHelperScenarios")
				cmd.Env = append(os.Environ(), "AGENTB_SPEECH_HELPER="+test.name)
				return cmd
			}
			response := httptest.NewRecorder()
			server.speechStreamHandler(response, httptest.NewRequest(http.MethodGet, "/api/speech/stream", nil))
			if !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("body=%s", response.Body.String())
			}
			if len(journal) < 2 || journal[0].Type != events.Speech {
				t.Fatalf("journal=%+v", journal)
			}
		})
	}
}
