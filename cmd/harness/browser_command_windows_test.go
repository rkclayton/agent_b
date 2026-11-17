//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/quietproc"
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

func TestBrowserVisitSurvivesRestart2yj(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor")
		} else {
			http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture", MaxAge: 3600, Path: "/"})
			fmt.Fprint(w, "first visitor")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	statePath, profile := filepath.Join(root, "state.json"), filepath.Join(root, "profile")
	for i := 0; i < 2; i++ {
		if err := startBrowser(statePath, profile, root, true); err != nil {
			t.Fatal(err)
		}
		state, _ := readBrowserState(statePath)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		socket, err := connectBrowser(ctx, state.Port)
		if err != nil {
			t.Fatal(err)
		}
		if err = browserVerb(ctx, socket, root, []string{"nav", server.URL}); err != nil {
			t.Fatal(err)
		}
		value, err := browserEval(ctx, socket, `document.body.innerText`)
		socket.Close()
		cancel()
		stopBrowser(statePath, profile, state, true)
		want := "first visitor"
		if i == 1 {
			want = "returning visitor"
		}
		if err != nil || fmt.Sprint(value) != want {
			t.Fatalf("visit %d = %v, want %s; error=%v", i, value, want, err)
		}
	}
}

func TestKeptBrowserProfileUsesOrdinaryToolBoundary2yj(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	profile := filepath.Join(root, "profile-data", "browser", "profile")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	cookie := filepath.Join(profile, "cookie-fixture.txt")
	if err := os.WriteFile(cookie, []byte("planted-cookie-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	item := &session.Session{ID: "fixture", Workspace: workspace, LastSeen: map[string]time.Time{}}
	ctx := context.Background()
	checks := []struct {
		name string
		tool tools.Tool
		args map[string]any
	}{{"read", tools.NewReadFile(cfg.Tools.ReadFile), map[string]any{"path": cookie}}, {"list", tools.NewListDir(cfg.Tools.ListDir), map[string]any{"path": profile}}, {"search", tools.NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir), map[string]any{"path": profile, "pattern": "planted-cookie"}}}
	for _, check := range checks {
		result, err := check.tool.Call(ctx, item, check.args)
		if err != nil || !(strings.Contains(result, "planted-cookie") || strings.Contains(result, "cookie-fixture")) {
			t.Errorf("%s did not use the ordinary operator filesystem boundary", check.name)
		}
	}
	cfg.Shell.OperatorContext = true
	result, err := tools.NewShell(cfg.Shell).Call(ctx, item, map[string]any{"command": "Get-Content -LiteralPath '" + strings.ReplaceAll(cookie, "'", "''") + "'"})
	if err != nil || !strings.Contains(result, "planted-cookie") {
		t.Error("shell did not use the ordinary operator filesystem boundary")
	}
}

func TestBrowserSeparateTabs2yj(t *testing.T) {
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			return
		}
		ready <- struct{}{}
		<-release
		fmt.Fprint(w, r.URL.Path)
	}))
	defer server.Close()
	root := t.TempDir()
	statePath, profile := filepath.Join(root, "state.json"), filepath.Join(root, "profile")
	if err := startBrowser(statePath, profile, root); err != nil {
		t.Fatal(err)
	}
	state, _ := readBrowserState(statePath)
	defer stopBrowser(statePath, profile, state)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	results := make(chan string, 2)
	for _, name := range []string{"/first", "/second"} {
		go func(name string) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", state.Port), nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- err.Error()
				return
			}
			var target struct{ WebSocketDebuggerURL string }
			json.NewDecoder(resp.Body).Decode(&target)
			resp.Body.Close()
			socket, err := connectBrowserPage(ctx, target.WebSocketDebuggerURL)
			if err != nil {
				results <- err.Error()
				return
			}
			defer socket.Close()
			socket.call(ctx, "Page.navigate", map[string]any{"url": server.URL + name})
			for {
				value, _ := browserEval(ctx, socket, `document.body?document.body.innerText:""`)
				if fmt.Sprint(value) == name {
					results <- name
					return
				}
				select {
				case <-ctx.Done():
					results <- ctx.Err().Error()
					return
				case <-time.After(25 * time.Millisecond):
				}
			}
		}(name)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			close(release)
			t.Fatal("two pages did not arrive concurrently")
		}
	}
	close(release)
	a, b := <-results, <-results
	if a == b || !(a == "/first" || a == "/second") || !(b == "/first" || b == "/second") {
		t.Fatalf("pages=%q/%q", a, b)
	}
}

func TestKeptBrowserLifecycle2yj(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			fmt.Fprint(w, strings.Repeat("x", 70000))
			return
		}
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor "+r.URL.Path)
		} else {
			http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture", MaxAge: 3600, Path: "/"})
			fmt.Fprint(w, "first visitor "+r.URL.Path)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	b := &keptBrowser{root: root}
	t.Cleanup(b.Close)
	statePath, profile := b.paths()
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("profile exists before first use")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	text, err := b.Fetch(ctx, server.URL+"/one")
	if err != nil || !strings.Contains(text, "first visitor") {
		t.Fatalf("first fetch: %q %v", text, err)
	}
	pid := b.state.PID
	created := processCreated(pid)
	if !processRunning(pid, created) {
		t.Fatal("browser stopped after fetch")
	}
	text, err = b.Fetch(ctx, server.URL+"/two")
	if err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("second fetch: %q %v", text, err)
	}
	if b.state.PID != pid {
		t.Fatal("browser restarted between fetches")
	}
	results := make(chan string, 2)
	for _, suffix := range []string{"/left", "/right"} {
		go func(suffix string) {
			text, err := b.Fetch(ctx, server.URL+suffix)
			if err != nil {
				results <- err.Error()
			} else {
				results <- text
			}
		}(suffix)
	}
	a, c := <-results, <-results
	if a == c || !strings.Contains(a, "returning visitor") || !strings.Contains(c, "returning visitor") {
		t.Fatalf("concurrent pages=%q / %q", a, c)
	}
	started := time.Now()
	text, err = b.Fetch(ctx, server.URL+"/large")
	if err != nil || len(text) != 65536+len("\n[cut to 65,536 bytes]\n") {
		t.Fatalf("bounded text bytes=%d error=%v", len(text), err)
	}
	t.Logf("full output bound: %d bytes in %s", len(text), time.Since(started))
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", b.state.Port))
	if err != nil {
		t.Fatal(err)
	}
	var tabs []map[string]any
	json.NewDecoder(response.Body).Decode(&tabs)
	response.Body.Close()
	pages := 0
	for _, tab := range tabs {
		if tab["type"] == "page" {
			pages++
		}
	}
	if pages != 1 {
		t.Fatalf("fetch pages left behind: %d", pages)
	}
	b.mu.Lock()
	b.timer.Reset(time.Millisecond)
	b.mu.Unlock()
	deadline := time.Now().Add(6 * time.Second)
	for processRunning(pid, created) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processRunning(pid, created) {
		t.Fatal("idle browser remains")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatal("idle removed profile")
	}
	text, err = b.Fetch(ctx, server.URL+"/idle-restart")
	if err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("idle restart: %q %v", text, err)
	}
	pid = b.state.PID
	created = processCreated(pid)
	b.Close()
	if processRunning(pid, created) {
		t.Fatal("browser remains after quit")
	}
	restarted := &keptBrowser{root: root}
	defer restarted.Close()
	text, err = restarted.Fetch(ctx, server.URL+"/app-restart")
	if err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("app restart: %q %v", text, err)
	}
	pid = restarted.state.PID
	created = processCreated(pid)
	browserWatchOwner(statePath, 0, 0)
	if processRunning(pid, created) {
		t.Fatal("orphan remains after owner death")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatal("owner death removed profile")
	}
	restarted.state = browserState{}
	recovered := &keptBrowser{root: root}
	defer recovered.Close()
	text, err = recovered.Fetch(ctx, server.URL+"/crash-recovery")
	if err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("owner recovery: %q %v", text, err)
	}
}

func TestBrowserLaunchArgumentsUnchanged2yj(t *testing.T) {
	want := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--remote-debugging-port=12345", "--remote-debugging-address=127.0.0.1", "--user-data-dir=profile", "--window-size=1280,800", "--disk-cache-size=67108864", "--media-cache-size=16777216", "about:blank"}
	if strings.Join(browserLaunchArgs(12345, "profile"), "\x00") != strings.Join(want, "\x00") {
		t.Fatal("browser launch arguments changed")
	}
}

func TestBrowserProfileOnlyContainsBrowserFiles2yj(t *testing.T) {
	root := t.TempDir()
	statePath, profile := filepath.Join(root, "managed", "state.json"), filepath.Join(root, "managed", "profile")
	if err := startBrowser(statePath, profile, root, true); err != nil {
		t.Fatal(err)
	}
	state, _ := readBrowserState(statePath)
	defer stopBrowser(statePath, profile, state, true)
	bare := filepath.Join(root, "bare")
	edge, err := findEdge()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	pid, err := startBrowserDetached(edge, browserLaunchArgs(port, bare), root)
	if err != nil {
		t.Fatal(err)
	}
	defer stopBrowser(filepath.Join(root, "absent-state"), bare, browserState{PID: pid, Port: port, Created: processCreated(pid)}, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if _, err := browserPageURL(ctx, port); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	top := func(path string) []string {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}
	a, b := top(profile), top(bare)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Join(a, "\n") != strings.Join(b, "\n") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		a, b = top(profile), top(bare)
	}
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("managed browser files differ from bare launch: %v / %v", a, b)
	}
}

func TestLegacyBrowserMigration2yj(t *testing.T) {
	data, legacy := t.TempDir(), t.TempDir()
	old := filepath.Join(legacy, "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(old, "profile"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyBrowserProfiles(data, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("legacy profile remains")
	}
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyBrowserProfiles(data, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("migration did not run only once")
	}
}

func TestLegacyBrowserMigrationShortRoot(t *testing.T) {
	legacy := t.TempDir()
	name, _ := syscall.UTF16PtrFromString(legacy)
	short := make([]uint16, 32768)
	if _, err := syscall.GetShortPathName(name, &short[0], uint32(len(short))); err != nil {
		t.Fatal(err)
	}
	alias := syscall.UTF16ToString(short)
	if strings.EqualFold(alias, legacy) {
		t.Skip("volume does not provide a short path alias")
	}
	old := filepath.Join(legacy, "0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(old, "profile"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyBrowserProfiles(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("legacy profile remains")
	}
}

func TestFetchUsesKeptBrowser2yj(t *testing.T) {
	var captured bytes.Buffer
	prior := log.Writer()
	log.SetOutput(&captured)
	defer log.SetOutput(prior)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor")
		} else {
			http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture-browser-cookie-secret", MaxAge: 3600})
			fmt.Fprint(w, "first visitor")
		}
	}))
	defer server.Close()
	root := t.TempDir()
	b := &keptBrowser{root: root}
	defer b.Close()
	cfg := config.Defaults(root).Tools.Fetch
	cfg.AllowInternalHosts = []string{"127.0.0.1"}
	f := tools.NewFetch(cfg)
	f.SetBrowser(b.Fetch)
	for _, want := range []string{"first visitor", "returning visitor"} {
		result := f.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL, "via": "browser"})
		if result.Err != nil || !strings.Contains(result.Content, want) {
			t.Fatalf("fetch: %v %q", result.Err, result.Content)
		}
		if strings.Contains(result.Content, root) || strings.Contains(result.Content, "fixture-browser-cookie-secret") || strings.Contains(captured.String(), "fixture-browser-cookie-secret") {
			t.Fatal("fetch exposed browser storage")
		}
	}
}

func TestBrowserProfileAtCacheWorkload2yj(t *testing.T) {
	css := "/*" + strings.Repeat("x", 1<<20) + "*/"
	served := atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cache" {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			served.Add(int64(len(css)))
			fmt.Fprint(w, css)
			return
		}
		fmt.Fprint(w, "<html><head>")
		for i := 0; i < 80; i++ {
			fmt.Fprintf(w, "<link rel=stylesheet href='/cache?n=%d'>", i)
		}
		fmt.Fprint(w, "</head><body>cache bound fixture</body></html>")
	}))
	defer server.Close()
	b := &keptBrowser{root: t.TempDir()}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	started := time.Now()
	text, err := b.Fetch(ctx, server.URL)
	if err != nil || !strings.Contains(text, "cache bound fixture") {
		t.Fatalf("cache fixture: %v", err)
	}
	statePath, profile := b.paths()
	stateInfo, stateErr := os.Stat(statePath)
	if stateErr != nil || stateInfo.Size() > 512 {
		t.Fatal("browser state metadata exceeded its fixed bound")
	}
	b.Close()
	var bytes int64
	err = filepath.Walk(filepath.Join(profile, "Default", "Cache"), func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if served.Load() < 80<<20 || bytes == 0 {
		t.Fatalf("cache bound: served=%d disk=%d", served.Load(), bytes)
	}
	t.Logf("configured 64MiB disk cache: served=%d bytes retained=%d bytes elapsed=%s", served.Load(), bytes, time.Since(started))
}

func TestBrowserCacheSetting2ym(t *testing.T) {
	served := atomic.Int64{}
	css := "/*" + strings.Repeat("x", 1<<20) + "*/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cache" {
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			served.Add(int64(len(css)))
			fmt.Fprint(w, css)
			return
		}
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture", MaxAge: 86400})
		fmt.Fprint(w, "<html><head>")
		for n := 0; n < 400; n++ {
			fmt.Fprintf(w, "<link rel=stylesheet href='/cache?n=%d'>", n)
		}
		fmt.Fprint(w, "</head><body>cache loaded</body></html>")
	}))
	defer server.Close()
	b := &keptBrowser{root: t.TempDir()}
	defer b.Close()
	_, profile := b.paths()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := b.Fetch(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	if served.Load() < 400<<20 {
		t.Fatalf("only %d bytes served", served.Load())
	}
	if text, err := b.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("cookie lost: %s %v", text, err)
	}
	// Allow the browser's asynchronous eviction to settle after closing the loading tab.
	time.Sleep(5 * time.Second)
	b.Close()
	var size int64
	err := filepath.Walk(filepath.Join(profile, "Default", "Cache"), func(_ string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stopped cache: served=%d cache=%d bytes", served.Load(), size)
	if size > 256<<20 {
		t.Fatalf("cache exceeds 256 MiB: %d", size)
	}
	next := &keptBrowser{root: b.root}
	defer next.Close()
	if text, err := next.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatal("cookie after cleanup", text, err)
	}
}

func TestBrowserWatcherHelper2yj(t *testing.T) {
	path := os.Getenv("AGENTB_BROWSER_WATCH_FIXTURE")
	if path == "" {
		return
	}
	owner, _ := strconv.Atoi(os.Getenv("AGENTB_BROWSER_WATCH_OWNER"))
	created, _ := strconv.ParseInt(os.Getenv("AGENTB_BROWSER_WATCH_CREATED"), 10, 64)
	browserWatchOwner(path, owner, created)
}

func TestBrowserDataClearAndAge2ym(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture", MaxAge: 365 * 86400})
		fmt.Fprint(w, "first visitor")
	}))
	defer server.Close()
	b := &keptBrowser{root: t.TempDir()}
	defer b.Close()
	data, ok := any(b).(interface{ Data(bool) (int64, error) })
	if !ok {
		t.Fatal("browser data read/clear is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if text, err := b.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "first visitor") {
		t.Fatalf("first visit: %s %v", text, err)
	}
	b.mu.Lock()
	b.stopLocked()
	b.mu.Unlock()
	_, profile := b.paths()
	old := time.Now().Add(-120 * 24 * time.Hour)
	for _, name := range []string{"History", filepath.Join("Network", "Cookies")} {
		if err := os.Chtimes(filepath.Join(profile, "Default", name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if text, err := b.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "returning visitor") {
		t.Fatalf("old cookie lost: %s %v", text, err)
	}
	if _, err := os.Stat(filepath.Join(profile, "Default", "History")); err != nil {
		t.Fatal(err)
	}
	if size, err := data.Data(false); err != nil || size == 0 {
		t.Fatalf("size=%d err=%v", size, err)
	}
	state := b.state
	if size, err := data.Data(true); err != nil || size != 0 {
		t.Fatalf("clear size=%d err=%v", size, err)
	}
	if processRunning(state.PID, state.Created) {
		t.Fatal("browser survived Clear")
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("profile survived Clear")
	}
	if size, err := data.Data(false); err != nil || size != 0 {
		t.Fatalf("empty size=%d err=%v", size, err)
	}
	if text, err := b.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "first visitor") {
		t.Fatalf("fresh visit: %s %v", text, err)
	}
}
func TestBrowserQuitWaitsForOwnedWatcher2yj(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fixture") }))
	defer server.Close()
	b := &keptBrowser{root: t.TempDir()}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := b.Fetch(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	statePath, _ := b.paths()
	command := quietproc.Quiet(exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBrowserWatcherHelper2yj$"))
	command.Env = append(os.Environ(), "AGENTB_BROWSER_WATCH_FIXTURE="+statePath, "AGENTB_BROWSER_WATCH_OWNER="+strconv.Itoa(os.Getpid()), "AGENTB_BROWSER_WATCH_CREATED="+strconv.FormatInt(processCreated(os.Getpid()), 10))
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	created := processCreated(pid)
	b.state.WatcherPID = pid
	b.state.WatcherCreated = created
	if err := writeBrowserState(statePath, b.state); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if processRunning(pid, created) {
		t.Error("owned watcher still holds the executable after close")
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheFoldersPreserveSession2yn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("visit"); err == nil {
			fmt.Fprint(w, "returning visitor")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "visit", Value: "fixture", MaxAge: 86400})
		fmt.Fprint(w, "first visitor")
	}))
	defer server.Close()
	b := &keptBrowser{root: t.TempDir()}
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Fetch(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	_, profile := b.paths()
	for _, name := range []string{"Default/Cache", "Default/Code Cache", "Default/GPUCache"} {
		b.mu.Lock()
		b.stopLocked()
		b.mu.Unlock()
		folder := filepath.Join(profile, filepath.FromSlash(name))
		if _, err := os.Stat(folder); err != nil {
			t.Fatal(name, err)
		}
		history := filepath.Join(profile, "Default", "History")
		before, err := os.ReadFile(history)
		if err != nil || !bytes.Contains(before, []byte(server.URL)) {
			t.Fatal("history missing", err)
		}
		if err := os.RemoveAll(folder); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(history)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("history changed on deletion", err)
		}
		if text, err := b.Fetch(ctx, server.URL); err != nil || !strings.Contains(text, "returning visitor") {
			t.Fatal("session damaged", text, err)
		}
		t.Logf("cache-only deletion and reopen: %s; history and returning cookie preserved", name)
	}
}

func TestCacheCleanupBoundaries2yn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fixture") }))
	defer server.Close()
	for _, mode := range []string{"idle", "quit", "start"} {
		t.Run(mode, func(t *testing.T) {
			b := &keptBrowser{root: t.TempDir()}
			defer b.Close()
			_, profile := b.paths()
			cache := filepath.Join(profile, "Default", "Cache")
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(cache, "bound-fixture")
			f, err := os.Create(marker)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Truncate(1024); err != nil {
				t.Fatal(err)
			}
			f.Close()
			b.mu.Lock()
			b.stopLocked()
			b.mu.Unlock()
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("under bound deleted", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if mode != "start" {
				if _, err := b.Fetch(ctx, server.URL); err != nil {
					t.Fatal(err)
				}
			}
			f, err = os.OpenFile(marker, os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Truncate(257 << 20); err != nil {
				t.Fatal(err)
			}
			f.Close()
			if mode != "start" {
				if _, err := b.Fetch(ctx, server.URL); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatal("running cache deleted", err)
				}
			}
			switch mode {
			case "idle":
				b.idle(b.generation)
			case "quit":
				b.Close()
			case "start":
				if _, err := b.Fetch(ctx, server.URL); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("oversize cache survived", err)
			}
		})
	}
}
