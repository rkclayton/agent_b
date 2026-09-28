//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Item 2mt (b), (c) and (d): THE DRAG DURING A STREAM, ON A DISPOSABLE INSTALL.
//
// The operator, 2026-09-27 03:11: "i dragged the window while he was thinking, it
// crashed." His chat s34 stops on a model.delta at 08:10:50.281Z, seq 6805, run
// r643 6,544 deltas in, and the whole process — server and host window together —
// went with it.
//
// (a)'s crash record and (e)'s cut-run close shipped in the first attempt. This is
// the reproduction, with the record in place so a crash names its own cause.
//
// WHAT THIS DRIVES, AND WHAT IT DELIBERATELY DOES NOT. A user's drag enters
// DefWindowProc's MODAL MOVE LOOP from WM_NCLBUTTONDOWN on the caption, and that
// loop reads real mouse input. Synthesising that means injecting global input into
// the operator's live session and stealing focus from whatever he is doing — which
// is the same family as the lock and disconnect proofs CLAUDE.md reserves to him,
// so this posts the message sequence a drag generates DIRECTLY TO THE WINDOW
// instead: the caption hit test, the entry and exit of a move, the WM_MOVING and
// WM_MOVE and WM_SIZE traffic, and real SetWindowPos moves. Everything the app's
// own windowProcedure sees in a drag, without touching anything outside this
// window. The one thing it cannot cover is the modal loop itself, and the report
// says so rather than implying a clean bill of health.
//
// Skipped unless AGENTB_DRAG_PROBE=1, because it starts a real windowed process:
// the installer matrix's window probe is what turns it on (d).
const (
	wmNCLButtonDown = 0x00A1
	wmNCLButtonUp   = 0x00A2
	wmEnterSizeMove = 0x0231
	wmMoving        = 0x0216
	wmMove          = 0x0003
	wmMouseMove     = 0x0200
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
)

// procPostMessage, procSendMessage and procGetWindowRect are already bound in
// host_window_windows.go; only these two are new here.
var (
	procFindWindowEx = user32.NewProc("FindWindowExW")
	procSetWindowPos = user32.NewProc("SetWindowPos")
)

type probeRect struct{ left, top, right, bottom int32 }

func findHostWindow(t *testing.T) uintptr {
	t.Helper()
	class, err := syscall.UTF16PtrFromString("Agent_b-host-window")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		hwnd, _, _ := procFindWindowEx.Call(0, 0, uintptr(unsafe.Pointer(class)), 0)
		if hwnd != 0 {
			return hwnd
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0
}

func freeProbePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port == 8790 {
		t.Fatal("the probe must never take production's port")
	}
	return port
}

// A model that streams for as long as the drag needs. Deltas are small and slow so
// the stream is certainly still open while the window is moved.
func streamingModel(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/props":
			_, _ = writer.Write([]byte(`{"connection":"drag-probe","n_ctx":32768}`))
			return
		case "/v1/models":
			_, _ = writer.Write([]byte(`{"data":[{"id":"drag-probe"}]}`))
			return
		case "/tokenize":
			_, _ = writer.Write([]byte(`{"tokens":[1]}`))
			return
		case "/apply-template":
			_, _ = writer.Write([]byte(`{"prompt":"drag probe"}`))
			return
		}
		if request.URL.Path != "/v1/chat/completions" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher, _ := writer.(http.Flusher)
		// Long enough to drag through: 120 reasoning deltas at 100ms is twelve
		// seconds of live stream, and the drag happens inside it.
		for index := 0; index < 120; index++ {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", `{"choices":[{"delta":{"reasoning_content":"weighing it up. "},"finish_reason":null}]}`)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
}

func TestDraggingTheHostWindowDuringAStreamDoesNotEndTheProcess(t *testing.T) {
	if os.Getenv("AGENTB_DRAG_PROBE") != "1" {
		t.Skip("set AGENTB_DRAG_PROBE=1: this starts a real windowed process")
	}
	root := t.TempDir()
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "Agent_b.exe")
	build := exec.Command(filepath.Join(repo, ".tools", "go", "bin", "go.exe"), "build", "-o", exe, "./cmd/harness")
	build.Dir = repo
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build: %v\n%s", buildErr, output)
	}

	// The host window needs its loader beside the executable, exactly as a real
	// install places it. Without this the instance says "host window: unavailable,
	// using the browser instead" and there is nothing to drag — which is what the
	// first two runs of this probe hit.
	loader, err := os.ReadFile(filepath.Join(repo, "WebView2Loader.dll"))
	if err != nil {
		t.Fatalf("the repository has no WebView2Loader.dll to place beside the probe binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "WebView2Loader.dll"), loader, 0o600); err != nil {
		t.Fatal(err)
	}

	model := streamingModel(t)
	defer model.Close()

	data := filepath.Join(root, "data")
	workspace := filepath.Join(root, "workspace")
	for _, dir := range []string{data, workspace} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	port := freeProbePort(t)
	// The shipped example is the base: a hand-written config misses required keys
	// and the instance refuses to start with nothing in the test's output to say
	// why, which is exactly what the first run of this probe did.
	example, err := os.ReadFile(filepath.Join(repo, "harness.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if unmarshalErr := json.Unmarshal(example, &config); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	config["listen"] = fmt.Sprintf("127.0.0.1:%d", port)
	config["workspace"] = workspace
	config["connections"] = []map[string]any{{
		"id": "drag-probe", "label": "drag probe", "base_url": model.URL, "model": "drag-probe",
		"request_timeout_s": 120,
	}}
	config["agents"] = []map[string]any{{"name": "agent_b", "b": "drag-probe", "c": "drag-probe"}}
	configPath := filepath.Join(data, "harness.json")
	body, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}

	app := exec.Command(exe, "-config", configPath, "-app-root", repo, "-data-root", data, "-window")
	app.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	childOutput := &strings.Builder{}
	app.Stdout, app.Stderr = childOutput, childOutput
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { _ = app.Wait(); close(stopped) }()
	defer func() {
		// Hard stop 12: a process this test started is ended by its PID and by
		// nothing else.
		select {
		case <-stopped:
		default:
			_ = app.Process.Kill()
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// A cookie jar, because /api/state is gated behind the launch bootstrap the
	// document hands out: without it the body comes back empty and the decode is
	// an EOF that says nothing about why.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	ready := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		response, getErr := client.Get(base + "/chat")
		if getErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("the disposable windowed instance never became ready; its own output:\n%s", childOutput.String())
	}

	hwnd := findHostWindow(t)
	if hwnd == 0 {
		t.Skipf("no host window appeared in 45s, so the drag cannot be driven; the instance said:\n%s", childOutput.String())
	}

	// The browser bootstrap, the same way test-updater-cycle.ps1 does it: the served
	// document carries the launch mutation token in a meta tag, and POST
	// /api/browser-session exchanges that token for the agentb_browser cookie that
	// /api/state and every mutation route require. Without it /api/message is a
	// bare 401 that says nothing.
	document, err := client.Get(base + "/chat")
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(document.Body)
	_ = document.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	token := regexp.MustCompile(`<meta name="agentb-mutation-token" content="([^"]+)">`).FindSubmatch(page)
	if token == nil {
		t.Fatalf("the served document carries no mutation token; %d bytes", len(page))
	}
	bootstrap, err := http.NewRequest("POST", base+"/api/browser-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Header.Set("X-AgentB-Mutation-Token", string(token[1]))
	session, err := client.Do(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Body.Close()
	if session.StatusCode != http.StatusOK && session.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /api/browser-session returned %d", session.StatusCode)
	}

	// Start the stream, then confirm it is actually live before dragging.
	state := struct {
		MutationToken string                    `json:"mutation_token"`
		Sessions      map[string]map[string]any `json:"sessions"`
	}{}
	response, err := client.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	raw, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if decodeErr := json.Unmarshal(raw, &state); decodeErr != nil {
		t.Fatalf("GET /api/state returned %d and %d bytes that are not the snapshot: %v; body: %.300s", response.StatusCode, len(raw), decodeErr, raw)
	}
	_ = response.Body.Close()
	sessionID := ""
	for id := range state.Sessions {
		sessionID = id
		break
	}
	if sessionID == "" {
		t.Fatal("the instance reported no session to talk to")
	}
	message, _ := json.Marshal(map[string]any{"session_id": sessionID, "text": "think for a while"})
	request, _ := http.NewRequest("POST", base+"/api/message", strings.NewReader(string(message)))
	request.Header.Set("Content-Type", "application/json")
	// The token from the document, which the bootstrap above already proved the
	// guard accepts. Reading it back out of the snapshot added a second thing that
	// could be wrong for the same 401.
	request.Header.Set("X-AgentB-Mutation-Token", string(token[1]))
	sent, err := client.Do(request)
	if err != nil {
		t.Fatalf("starting the stream: %v", err)
	}
	_ = sent.Body.Close()
	if sent.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/message returned %d", sent.StatusCode)
	}
	time.Sleep(1500 * time.Millisecond)

	// THE DRAG. Every message a caption drag puts through windowProcedure, plus
	// real moves, repeated while the stream runs.
	var box probeRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&box)))
	caption := uintptr(uint32(int32(box.left+80)) | uint32(int32(box.top+8))<<16)
	procSendMessage.Call(hwnd, wmNCHitTest, 0, caption)
	procPostMessage.Call(hwnd, wmNCLButtonDown, htCaption, caption)
	procPostMessage.Call(hwnd, wmEnterSizeMove, 0, 0)
	for step := 0; step < 24; step++ {
		left := box.left + int32(step%8)*3
		top := box.top + int32(step%5)*3
		var moving probeRect = probeRect{left: left, top: top, right: left + (box.right - box.left), bottom: top + (box.bottom - box.top)}
		procPostMessage.Call(hwnd, wmMoving, 0, uintptr(unsafe.Pointer(&moving)))
		procPostMessage.Call(hwnd, wmMouseMove, 0, uintptr(uint32(int32(40+step))|uint32(int32(6))<<16))
		procSetWindowPos.Call(hwnd, 0, uintptr(left), uintptr(top), 0, 0, swpNoZOrder|swpNoActivate|0x0001)
		procPostMessage.Call(hwnd, wmMove, 0, uintptr(uint32(left)|uint32(top)<<16))
		procSendMessage.Call(hwnd, wmNCHitTest, 0, caption)
		time.Sleep(60 * time.Millisecond)
	}
	procPostMessage.Call(hwnd, wmExitSizeMove, 0, 0)
	procPostMessage.Call(hwnd, wmNCLButtonUp, htCaption, caption)
	procSetWindowPos.Call(hwnd, 0, uintptr(box.left), uintptr(box.top), 0, 0, swpNoZOrder|swpNoActivate|0x0001)
	time.Sleep(2 * time.Second)

	// THE ASSERTIONS. The process is the same one, it still answers, and nothing
	// wrote a crash record.
	select {
	case <-stopped:
		crash := crashFilesUnder(t, data)
		t.Fatalf("the process ended during a drag through a live stream; crash records: %v", crash)
	default:
	}
	after, err := client.Get(base + "/api/state")
	if err != nil {
		t.Fatalf("the instance stopped answering after the drag: %v", err)
	}
	_ = after.Body.Close()
	if crash := crashFilesUnder(t, data); len(crash) > 0 {
		t.Fatalf("a crash record was written during the drag: %v", crash)
	}
	// And the launcher log gained no line about an unexplained end.
	if log, readErr := os.ReadFile(filepath.Join(data, "logs", "launcher-errors.log")); readErr == nil {
		if strings.Contains(string(log), "ended without recording a reason") {
			t.Fatalf("the launcher log records an unexplained end:\n%s", log)
		}
	}
}

func crashFilesUnder(t *testing.T, dataRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "logs"))
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		// A crash RECORD is crash-<stamp>.json. crash-stderr.log is the traceback
		// sink installCrashRecord opens on every windowed launch, so matching every
		// "crash-" prefix reported a crash on a run that had not crashed.
		if strings.HasPrefix(entry.Name(), "crash-") && strings.HasSuffix(entry.Name(), ".json") {
			found = append(found, entry.Name())
		}
	}
	return found
}
