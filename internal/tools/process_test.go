package tools

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

func TestProcessHTTPHelper(t *testing.T) {
	if os.Getenv("AGENTB_PROCESS_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Args[len(os.Args)-1])
	if err != nil {
		os.Exit(2)
	}
	fmt.Println("ready")
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			os.Exit(0)
		}
		reader := bufio.NewReader(connection)
		line, _ := reader.ReadString('\n')
		fmt.Print("request:" + line)
		_, _ = connection.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
		_ = connection.Close()
	}
}

func TestProcessEndGoalFixtureServer(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	shell := NewShell(processTestShell())
	tool := NewProcess(shell)
	s := &session.Session{ID: "fixture", Workspace: t.TempDir(), Run: session.RunState{RunID: "run"}}
	command := processTestCommand(
		fmt.Sprintf(`$env:AGENTB_PROCESS_HELPER='1'; & '%s' '-test.run=TestProcessHTTPHelper' -- %d`, strings.ReplaceAll(os.Args[0], "'", "''"), port),
		fmt.Sprintf(`AGENTB_PROCESS_HELPER=1 %s -test.run=TestProcessHTTPHelper -- %d`, strconv.Quote(os.Args[0]), port),
	)
	started, err := tool.Call(context.Background(), s, map[string]any{"action": "start", "command": command})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(started, "started ")
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _ := tool.Call(context.Background(), s, map[string]any{"action": "read", "id": id})
		if strings.Contains(out, "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/page", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	for {
		out, _ := tool.Call(context.Background(), s, map[string]any{"action": "read", "id": id})
		if strings.Contains(out, "GET /page HTTP/1.1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("request not logged: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = tool.Call(context.Background(), s, map[string]any{"action": "kill", "id": id}); err != nil {
		t.Fatal(err)
	}
	if out, err := tool.Call(context.Background(), s, map[string]any{"action": "wait", "id": id, "timeout_s": 5}); err != nil || !strings.Contains(out, "exit=") {
		t.Fatalf("wait=%q err=%v", out, err)
	}
}

func processTestShell() config.Shell {
	if runtime.GOOS == "windows" {
		return config.Shell{Command: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command"}, TimeoutS: 10, MaxTimeoutS: 10}
	}
	return config.Shell{Command: []string{"sh", "-c"}, TimeoutS: 10, MaxTimeoutS: 10}
}

func processTestCommand(windows, other string) string {
	if runtime.GOOS == "windows" {
		return windows
	}
	return other
}

func TestProcessStartReadWriteWaitAndKill(t *testing.T) {
	shell := NewShell(processTestShell())
	tool := NewProcess(shell)
	s := &session.Session{ID: "chat", Workspace: t.TempDir(), Run: session.RunState{RunID: "run"}}
	started, err := tool.Call(context.Background(), s, map[string]any{"action": "start", "command": processTestCommand(`$line=[Console]::In.ReadLine(); Write-Output "heard:$line"; Start-Sleep -Seconds 30`, `read line; echo "heard:$line"; sleep 30`)})
	if err != nil || !strings.HasPrefix(started, "started ") {
		t.Fatalf("start = %q, %v", started, err)
	}
	id := strings.TrimPrefix(started, "started ")
	if _, err = tool.Call(context.Background(), s, map[string]any{"action": "write", "id": id, "input": "hello\n"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, readErr := tool.Call(context.Background(), s, map[string]any{"action": "read", "id": id, "offset": 0})
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(out, "heard:hello") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output never arrived: %q", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = tool.Call(context.Background(), s, map[string]any{"action": "kill", "id": id}); err != nil {
		t.Fatal(err)
	}
	if out, err := tool.Call(context.Background(), s, map[string]any{"action": "wait", "id": id, "timeout_s": 2}); err != nil || !strings.Contains(out, "exit=") {
		t.Fatalf("wait = %q, %v", out, err)
	}
}

func TestProcessLimitAndOutputWindow(t *testing.T) {
	shell := NewShell(processTestShell())
	tool := NewProcess(shell)
	s := &session.Session{ID: "chat", Workspace: t.TempDir(), Run: session.RunState{RunID: "run"}}
	for i := 0; i < 4; i++ {
		if _, err := tool.Call(context.Background(), s, map[string]any{"action": "start", "command": processTestCommand(`Start-Sleep -Seconds 30`, `sleep 30`)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tool.Call(context.Background(), s, map[string]any{"action": "start", "command": processTestCommand(`Start-Sleep -Seconds 30`, `sleep 30`)}); err == nil {
		t.Fatal("fifth process was accepted")
	}
	tool.KillRun("chat", "run")

	w := newProcessWindow(1 << 20)
	chunk := strings.Repeat("x", 10<<20)
	if _, err := w.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	text, base, next := w.Read(0)
	if len(text) != 1<<20 || base != 9<<20 || next != 10<<20 {
		t.Fatalf("window len=%d base=%d next=%d", len(text), base, next)
	}
}

func TestProcessAndShellShareDenyList(t *testing.T) {
	cfg := processTestShell()
	cfg.Deny = []string{"blocked"}
	shell := NewShell(cfg)
	s := &session.Session{ID: "chat", Workspace: t.TempDir(), Run: session.RunState{RunID: "run"}}
	_, shellErr := shell.Call(context.Background(), s, map[string]any{"command": "blocked argument"})
	_, processErr := NewProcess(shell).Call(context.Background(), s, map[string]any{"action": "start", "command": "blocked argument"})
	if shellErr == nil || processErr == nil || shellErr.Error() != processErr.Error() {
		t.Fatalf("shell=%v process=%v", shellErr, processErr)
	}
}

func TestProcessCleanupForEveryRunEnd(t *testing.T) {
	for _, ending := range []string{"done", "stopped", "cancelled", "crashed", "app closing"} {
		t.Run(ending, func(t *testing.T) {
			shell := NewShell(processTestShell())
			tool := NewProcess(shell)
			s := &session.Session{ID: "chat", Workspace: t.TempDir(), Run: session.RunState{RunID: "run"}}
			started, err := tool.Call(context.Background(), s, map[string]any{"action": "start", "command": processTestCommand(`Start-Sleep -Seconds 30`, `sleep 30`)})
			if err != nil {
				t.Fatal(err)
			}
			id := strings.TrimPrefix(started, "started ")
			tool.mu.Lock()
			item := tool.items[id]
			tool.mu.Unlock()
			if ending == "app closing" {
				tool.KillAll()
			} else {
				New(tool).KillRunProcesses("chat", "run")
			}
			select {
			case <-item.done:
			case <-time.After(5 * time.Second):
				t.Fatal("process survived cleanup")
			}
		})
	}
}
