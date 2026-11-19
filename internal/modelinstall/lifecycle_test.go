package modelinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"harness/internal/quietproc"
	"harness/internal/updater"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMissingNativeImport2z4(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows loader")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("kernel32.dll\x00")) {
		t.Fatal("fixture import absent")
	}
	b = bytes.ReplaceAll(b, []byte("kernel32.dll\x00"), []byte("absentxx.dll\x00"))
	target := filepath.Join(t.TempDir(), "llama-server.exe")
	if err = os.WriteFile(target, b, 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = probeRuntime(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "0xC0000135") || !strings.Contains(err.Error(), "absentxx.dll") || time.Since(started) > 16*time.Second {
		t.Fatalf("missing DLL result: %v", err)
	}
	t.Logf("native missing-import exit verified in %s", time.Since(started))
}

// Opt-in native proof uses an already signed disposable fixture, never a redistributable.
func TestSignedDependencyFixture2z4(t *testing.T) {
	path := os.Getenv("AGENTB_DEPENDENCY_TEST_FIXTURE")
	if path == "" || runtime.GOOS != "windows" {
		t.Skip("signed disposable fixture not supplied")
	}
	sig, err := updater.InspectSignature(path)
	if err != nil || sig.Status != "Valid" || sig.Subject != "CN=Agent_b Disposable Test Signing" {
		t.Fatal("fixture must have the disposable test signer")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, exit := range []uint32{0, 1638, 0x80070666, 1602, 1223, 1603, 3010, 1641} {
		t.Run(fmt.Sprint(exit), func(t *testing.T) {
			m := New(t.TempDir(), "", nil, Runtime{}, nil, nil)
			var downloaded string
			m.dependencyClient = &http.Client{Transport: installRoundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: int64(len(b)), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
			})}
			m.verify = func(p string) bool {
				downloaded = p
				if err := os.WriteFile(p, []byte("replacement"), 0600); err == nil {
					t.Fatal("verified file could be replaced")
				}
				s, e := updater.InspectSignature(p)
				return e == nil && s.Status == "Valid" && s.Subject == sig.Subject
			}
			m.install = func(p string) (uint32, error) {
				cmd := quietproc.Quiet(exec.Command(p, strconv.FormatUint(uint64(exit), 10)))
				err := cmd.Run()
				if cmd.ProcessState != nil {
					return uint32(cmd.ProcessState.ExitCode()), nil
				}
				return 0, err
			}
			want := "dependency_failed"
			switch exit {
			case 0, 1638, 0x80070666:
				want = ""
			case 1602, 1223:
				want = "dependency_declined"
			case 3010, 1641:
				want = "dependency_restart"
			}
			if got := m.installDependency(context.Background()); got != want {
				t.Fatalf("result %s want %s", got, want)
			}
			if _, err := os.Stat(downloaded); !os.IsNotExist(err) {
				t.Fatal("signed download retained")
			}
			if microsoftSignature(path) {
				t.Fatal("production verifier trusts disposable signer")
			}
		})
	}
	if err := probeRuntime(context.Background(), path); err != nil {
		t.Fatalf("loadable native control failed: %v", err)
	}
	t.Setenv("AGENTB_TEST_RUNTIME_WAIT", "1")
	started := time.Now()
	err = probeRuntime(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "timeout") || time.Since(started) < 14*time.Second || time.Since(started) > 17*time.Second {
		t.Fatalf("15-second limit failed: %v", err)
	}
	t.Logf("native runtime timeout=%s", time.Since(started))
}

func TestCustomAlreadyAnsweringNeedsStopCommand2yb(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer server.Close()
	c := NewController(nil)
	defer c.Close()
	_ = c.Configure(Spec{ID: "custom", URL: server.URL, StartCommand: "must never run", IdleMinutes: 0})
	if err := c.Start(context.Background(), "custom"); err != nil {
		t.Fatal(err)
	}
	if c.State("custom").State != "running" {
		t.Fatal("not running")
	}
	if err := c.Stop("custom", "requested"); !errors.Is(err, ErrStopCommandRequired) {
		t.Fatal("unowned server needs explicit stop command")
	}
	if c.State("custom").State != "running" {
		t.Fatal("still answering server lost running state")
	}
}

func TestCommandConfigurationRetainsOnlyCurrentEntries2yb(t *testing.T) {
	c := NewController(nil)
	defer c.Close()
	for i := range 30 {
		_ = c.Configure(Spec{ID: fmt.Sprintf("saved-%d", i), StartCommand: "fixture", IdleMinutes: 0})
	}
	began := time.Now()
	for i := range 10000 {
		id := fmt.Sprint(i)
		_ = c.Configure(Spec{ID: id, StartCommand: "fixture", IdleMinutes: 0})
		c.UnconfigureCommand(id)
	}
	if len(c.States()) != 30 {
		t.Fatal("removed command configurations retained")
	}
	t.Logf("30 configured connections, 10000 configure/remove cycles: %s, retained entries=30", time.Since(began))
	for _, state := range c.States() {
		c.UnconfigureCommand(state.ConnectionID)
	}
	if len(c.States()) != 0 {
		t.Fatal("cleared table retains entries")
	}
}

func TestManagedMigrationOnlyOwnLauncher(t *testing.T) {
	root, startup := t.TempDir(), t.TempDir()
	command := filepath.Join(root, "start-model.cmd")
	if err := os.WriteFile(command, []byte("saved command"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(startup, "Agent_b-model.vbs")
	for _, own := range []bool{false, true} {
		body := []byte("foreign")
		if own {
			body = legacyLauncher(command)
		}
		if err := os.WriteFile(launcher, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := removeLegacyLauncher(root, startup); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(launcher)
		if os.IsNotExist(err) != own {
			t.Fatalf("own=%v removed=%v", own, os.IsNotExist(err))
		}
	}
}

func TestManagedStopWhileLoadingAndBoundedStarts(t *testing.T) {
	c := NewController(nil)
	defer c.Close()
	started := make(chan struct{})
	launches := 0
	alive := false
	c.find = func(Spec) (*managedProcess, error) { return nil, nil }
	c.launch = func(Spec) (*managedProcess, error) {
		launches++
		alive = true
		return &managedProcess{stop: func() error { alive = false; return nil }, exited: func() bool { return !alive }}, nil
	}
	c.health = func(ctx context.Context, _ Spec) error { close(started); <-ctx.Done(); return ctx.Err() }
	_ = c.Configure(Spec{ID: "installed-local", IdleMinutes: 60})
	if err := c.BeginStart("installed-local"); err != nil {
		t.Fatal(err)
	}
	<-started
	began := time.Now()
	for range 10000 {
		if err := c.BeginStart("installed-local"); err != nil {
			t.Fatal(err)
		}
	}
	allocations := testing.AllocsPerRun(1000, func() { _ = c.BeginStart("installed-local") })
	if allocations != 0 {
		t.Fatalf("repeated start allocates %g objects", allocations)
	}
	if err := c.Stop("installed-local", "requested"); err != nil {
		t.Fatal(err)
	}
	if alive || launches != 1 || len(c.entries) != 1 || c.State("installed-local").State != "stopped" {
		t.Fatal("loading stop or bounded start failed")
	}
	t.Logf("10000 start requests at the full one-model bound: %s, launches=%d, retained entries=%d, allocations/request=%g", time.Since(began), launches, len(c.entries), allocations)
}

func TestManagedStandIn(t *testing.T) {
	args := os.Args
	if len(args) >= 3 && args[len(args)-2] == "--model-stop" {
		response, err := http.Get(args[len(args)-1] + "/stop")
		if err == nil {
			response.Body.Close()
		}
		os.Exit(0)
	}
	if len(args) < 3 || args[len(args)-2] != "--model-standin" {
		return
	}
	_ = http.ListenAndServe("127.0.0.1:"+args[len(args)-1], http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stop" {
			go func() { time.Sleep(10 * time.Millisecond); os.Exit(0) }()
		}
		_, _ = io.WriteString(w, "ok")
	}))
	os.Exit(0)
}

func TestSavedCommandTreeStopAndIdle2yb(t *testing.T) {
	root := t.TempDir()
	executable, _ := os.Executable()
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "stand-in")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, content, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"idle", "stop-command", "already-running"} {
		t.Run(mode, func(t *testing.T) {
			explicit := mode != "idle"
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			s := Spec{ID: "command", Root: root, URL: fmt.Sprintf("http://127.0.0.1:%d", port), IdleMinutes: 1, StartCommand: fmt.Sprintf(`"%s" -test.run=TestManagedStandIn -- --model-standin %d`, path, port)}
			if explicit {
				s.StopCommand = fmt.Sprintf(`"%s" -test.run=TestManagedStandIn -- --model-stop %s`, path, s.URL)
			}
			if mode == "already-running" {
				outside, err := launchCommand(s)
				if err != nil {
					t.Fatal(err)
				}
				defer outside.stop()
				if err := waitHealth(context.Background(), &http.Client{Timeout: time.Second}, s.URL+"/health", 10*time.Second); err != nil {
					t.Fatal(err)
				}
			}
			c := NewController(nil)
			defer c.Close()
			if err := c.Configure(s); err != nil {
				t.Fatal(err)
			}
			release, err := c.Acquire(context.Background(), s.ID)
			if err != nil {
				t.Fatal(err)
			}
			if c.State(s.ID).State != "running" {
				t.Fatal("command not ready")
			}
			release()
			if explicit {
				if err := c.Stop(s.ID, "requested"); err != nil {
					t.Fatal(err)
				}
			} else {
				c.mu.Lock()
				c.entries[s.ID].last = time.Now().Add(-2 * time.Minute)
				c.mu.Unlock()
				c.Tick()
			}
			if c.State(s.ID).State != "stopped" || commandReady(context.Background(), s) {
				t.Fatal("launched child survived stop")
			}
		})
	}
}
func TestManagedExecutableOwnershipAdoptionAndQuit(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(root, "model-fixture.exe")
	if err = os.WriteFile(own, b, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	s := Spec{ID: "installed-local", Root: root, Executable: own, Args: []string{"-test.run=TestManagedStandIn", "--", "--model-standin", fmt.Sprint(port)}, URL: fmt.Sprintf("http://127.0.0.1:%d", port), IdleMinutes: 60}
	if err = SaveSpec(s); err != nil {
		t.Fatal(err)
	}
	s, err = LoadSpec(root, "", s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		cmd := quietproc.Quiet(exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", `(New-Object -ComObject Scripting.FileSystemObject).GetFolder($env:MODEL_FIXTURE_ROOT).ShortPath`))
		cmd.Env = append(os.Environ(), "MODEL_FIXTURE_ROOT="+root)
		short, err := cmd.Output()
		if err != nil {
			t.Fatal("fixture short path unavailable")
		}
		alias := strings.TrimSpace(string(short))
		if alias == "" {
			t.Fatal("fixture short path empty")
		}
		s.Root = alias
		s.Executable = filepath.Join(alias, filepath.Base(own))
		t.Logf("short path spelling differs=%v", !strings.EqualFold(alias, root))
	}
	c := NewController(nil)
	defer c.Close()
	if err = c.Configure(s); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(context.Background(), s.ID); err != nil {
		t.Fatal(err)
	}
	if c.State(s.ID).State != "running" {
		t.Fatal("not ready")
	}
	if runtime.GOOS == "windows" {
		foreign := s
		foreign.Root = t.TempDir()
		if p, err := findManaged(foreign); err == nil {
			if p != nil {
				_ = p.stop()
			}
			t.Fatal("foreign process adopted")
		}
		response, err := http.Get(s.URL + "/health")
		if err != nil {
			t.Fatal("foreign ownership check ended the listener")
		}
		response.Body.Close()
		adopted := NewController(nil)
		defer adopted.Close()
		_ = adopted.Configure(s)
		if err = adopted.Adopt(s.ID); err != nil {
			t.Fatal(err)
		}
		if adopted.State(s.ID).State != "running" {
			t.Fatal("did not adopt")
		}
		adopted.Close()
	}
	c.Close()
	if _, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		t.Fatal("quit left listener alive")
	}
	foreign := s
	foreign.Executable = exe
	if p, err := launchManaged(foreign); err == nil {
		_ = p.stop()
		t.Fatal("foreign executable started")
	}
	t.Logf("stand-in executable bytes=%d; one managed entry, no retained history", len(b))
}

func TestManagedReplacementKeepsOldUntilNewReady(t *testing.T) {
	root := t.TempDir()
	original, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	spec := func(name string) Spec {
		t.Helper()
		exe := filepath.Join(root, name+".exe")
		if err := os.WriteFile(exe, body, 0700); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return Spec{ID: "installed-local", Root: root, Executable: exe, Args: []string{"-test.run=TestManagedStandIn", "--", "--model-standin", fmt.Sprint(port)}, URL: fmt.Sprintf("http://127.0.0.1:%d", port), IdleMinutes: 60, Files: []string{exe}}
	}
	old, next := spec("old"), spec("next")
	c := NewController(nil)
	defer c.Close()
	_ = c.Configure(old)
	if err := c.Start(context.Background(), old.ID); err != nil {
		t.Fatal(err)
	}
	p, err := launchManaged(next)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	if err = waitHealth(context.Background(), http.DefaultClient, next.URL+"/health", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(old.URL + "/health")
	if err != nil {
		t.Fatal("old model stopped before replacement ready")
	}
	response.Body.Close()
	if err = c.Replace(next); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" && c.State(next.ID).State != "running" {
		t.Fatal("replacement not adopted")
	}
	if _, err = os.Stat(old.Executable); !os.IsNotExist(err) {
		t.Fatal("previous downloaded executable retained")
	}
	if _, err = os.Stat(next.Executable); err != nil {
		t.Fatal("replacement deleted")
	}
}
func TestManagedFailureAndIdleChoices(t *testing.T) {
	for _, minutes := range []int{5, 0} {
		c := NewController(nil)
		now := time.Unix(0, 0)
		c.mu.Lock()
		c.now = func() time.Time { return now }
		c.mu.Unlock()
		alive := false
		c.find = func(Spec) (*managedProcess, error) { return nil, nil }
		c.launch = func(Spec) (*managedProcess, error) {
			alive = true
			return &managedProcess{stop: func() error { alive = false; return nil }, exited: func() bool { return !alive }}, nil
		}
		c.health = func(context.Context, Spec) error { return nil }
		_ = c.Configure(Spec{ID: "installed-local", IdleMinutes: minutes})
		if err := c.Start(context.Background(), "installed-local"); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		now = now.Add(5 * time.Minute)
		c.mu.Unlock()
		c.Tick()
		if alive != (minutes == 0) {
			t.Fatalf("idle=%d alive=%v", minutes, alive)
		}
		c.Close()
	}
	c := NewController(nil)
	defer c.Close()
	c.find = func(Spec) (*managedProcess, error) { return nil, errors.New("unowned listener") }
	_ = c.Configure(Spec{ID: "installed-local", IdleMinutes: 60})
	if err := c.Start(context.Background(), "installed-local"); err == nil || c.State("installed-local").Reason != "ownership" {
		t.Fatal("ownership failure missing")
	}
	if err := c.Configure(Spec{ID: "installed-local", IdleMinutes: -1}); err == nil {
		t.Fatal("negative idle accepted")
	}
	c.find = func(Spec) (*managedProcess, error) { return nil, nil }
	alive := false
	c.launch = func(Spec) (*managedProcess, error) {
		alive = true
		return &managedProcess{stop: func() error { alive = false; return nil }, exited: func() bool { return !alive }}, nil
	}
	c.health = func(context.Context, Spec) error { return errors.New("health timeout") }
	if err := c.Start(context.Background(), "installed-local"); err == nil || alive || c.State("installed-local").Reason != "health" {
		t.Fatal("health failure retained process or lost reason")
	}
}

func TestManagedStartStopAndIdle(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewController(nil)
	c.mu.Lock()
	c.now = func() time.Time { return now }
	c.mu.Unlock()
	defer c.Close()
	alive := false
	starts := 0
	c.launch = func(Spec) (*managedProcess, error) {
		alive = true
		starts++
		return &managedProcess{stop: func() error { alive = false; return nil }, exited: func() bool { return !alive }}, nil
	}
	c.find = func(Spec) (*managedProcess, error) { return nil, nil }
	c.health = func(context.Context, Spec) error { return nil }
	if err := c.Configure(Spec{ID: "installed-local", Root: t.TempDir(), IdleMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	release, err := c.Acquire(context.Background(), "installed-local")
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	now = now.Add(61 * time.Minute)
	c.mu.Unlock()
	c.Tick()
	if !alive {
		t.Fatal("stopped active run")
	}
	release()
	c.mu.Lock()
	now = now.Add(59 * time.Minute)
	c.mu.Unlock()
	c.Tick()
	if !alive {
		t.Fatal("stopped early")
	}
	c.mu.Lock()
	now = now.Add(time.Minute)
	c.mu.Unlock()
	c.Tick()
	if alive || c.State("installed-local").State != "stopped" {
		t.Fatal("idle did not stop")
	}
	if err := c.Start(context.Background(), "installed-local"); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatal("on demand failed")
	}
	if err := c.Stop("installed-local", "requested"); err != nil {
		t.Fatal(err)
	}
	if alive {
		t.Fatal("stop did not wait")
	}
}
