//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
	"harness/internal/tools"
)

func TestBrowserReadsDrivesAndCapturesScriptedPage2qt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Fixture page</title><input id="name"><button id="submit">Submit</button><button id="pick">Choose me</button><div id="out"></div><script>const input=document.querySelector('#name'),result=document.querySelector('#out');result.textContent='script-only text';submit.onclick=()=>result.textContent='clicked:'+input.value;pick.onclick=()=>result.textContent='picked';input.onkeydown=e=>{if(e.key==='Enter')result.textContent='pressed:'+input.value}</script>`)
	}))
	defer server.Close()
	workspace := t.TempDir()
	statePath, profile := browserPaths(workspace)
	if err := startBrowser(statePath, profile, workspace); err != nil {
		t.Fatal(err)
	}
	state, _ := readBrowserState(statePath)
	created := processCreated(state.PID)
	t.Cleanup(func() { killBrowserProcess(state.PID); _ = os.RemoveAll(filepath.Dir(statePath)) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	socket, err := connectBrowser(ctx, state.Port)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := browserVerb(ctx, socket, workspace, []string{"nav", server.URL}); err != nil {
		t.Fatal(err)
	}
	fetchConfig := config.Defaults(workspace).Tools.Fetch
	fetchConfig.AllowInternalHosts = []string{"127.0.0.1"}
	fetched := tools.NewFetch(fetchConfig).CallDetailed(ctx, &session.Session{}, map[string]any{"url": server.URL})
	if fetched.Err != nil || strings.Contains(fetched.Content, "script-only text") {
		t.Fatalf("fetch_url content=%q err=%v", fetched.Content, fetched.Err)
	}
	text, err := browserEval(ctx, socket, `document.body.innerText`)
	if err != nil || !strings.Contains(fmt.Sprint(text), "script-only text") {
		t.Fatalf("text=%v err=%v", text, err)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"fill", "#name", "Ada"}); err != nil {
		t.Fatal(err)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"click", "#submit"}); err != nil {
		t.Fatal(err)
	}
	value, _ := browserEval(ctx, socket, `out.textContent`)
	if value != "clicked:Ada" {
		t.Fatalf("click result=%v", value)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"press", "Enter"}); err != nil {
		t.Fatal(err)
	}
	value, _ = browserEval(ctx, socket, `out.textContent`)
	if value != "pressed:Ada" {
		t.Fatalf("press result=%v", value)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"pick", "Choose me"}); err != nil {
		t.Fatal(err)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"wait", "#out"}); err != nil {
		t.Fatal(err)
	}
	if err := browserVerb(ctx, socket, workspace, []string{"screenshot"}); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(workspace, "screenshots", "browser-*.png"))
	if len(matches) != 1 {
		t.Fatalf("screenshots=%v", matches)
	}
	if !strings.HasPrefix(browserUsage(), "usage: browser start") {
		t.Fatal(browserUsage())
	}
	stopBrowser(statePath, profile, state)
	deadline := time.Now().Add(5 * time.Second)
	for processRunning(state.PID, created) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processRunning(state.PID, created) {
		t.Fatalf("browser process %d remains after stop", state.PID)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("browser state remains after stop: %v", err)
	}
}
