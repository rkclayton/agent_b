package modelinstall

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Spec is kept on the PC. Only ModelState crosses the paired-device boundary.
type Spec struct {
	ID, Root, Executable, URL, Label string
	Args, Files                      []string
	Bytes                            int64
	IdleMinutes                      int
	StartCommand, StopCommand        string `json:"-"`
	APIKey                           string `json:"-"`
}
type ModelState struct {
	ConnectionID string `json:"connection_id"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	Model        string `json:"model"`
	Bytes        int64  `json:"bytes"`
}
type managedProcess struct {
	stop        func() error
	exited      func() bool
	commandStop func(string) error
	release     func()
}

func (e *managedEntry) stopProcess() error {
	if e.process.commandStop != nil {
		return e.process.commandStop(e.spec.StopCommand)
	}
	return e.process.stop()
}

type managedEntry struct {
	asyncDone   chan struct{}
	asyncCancel context.CancelFunc
	removing    bool
	spec        Spec
	state       ModelState
	process     *managedProcess
	cancel      context.CancelFunc
	done        chan struct{}
	active      int
	last        time.Time
}
type Controller struct {
	mu           sync.Mutex
	entries      map[string]*managedEntry
	changed      func(ModelState)
	now          func() time.Time
	launch, find func(Spec) (*managedProcess, error)
	health       func(context.Context, Spec) error
	quit         chan struct{}
	once         sync.Once
}

func NewController(changed func(ModelState)) *Controller {
	c := &Controller{entries: map[string]*managedEntry{}, changed: changed, now: time.Now, launch: launchManaged, find: findManaged, quit: make(chan struct{})}
	c.health = func(ctx context.Context, s Spec) error {
		if s.StartCommand != "" {
			deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			for {
				if commandReady(deadline, s) {
					return nil
				}
				select {
				case <-deadline.Done():
					return deadline.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		return waitHealth(ctx, &http.Client{Timeout: time.Second}, s.URL+"/health", 60*time.Second)
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				c.Tick()
			case <-c.quit:
				return
			}
		}
	}()
	return c
}
func (c *Controller) Configure(s Spec) error {
	if s.ID == "" || s.IdleMinutes < 0 {
		return errors.New("stop when idle must be a whole number of minutes, 0 or more")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[s.ID]; e != nil {
		e.spec.IdleMinutes = s.IdleMinutes
		if s.StartCommand != "" {
			e.spec = s
		}
		return nil
	}
	// Connections are bounded by configuration; no historical states are retained.
	c.entries[s.ID] = &managedEntry{spec: s, state: ModelState{s.ID, "stopped", "requested", s.Label, s.Bytes}, last: c.now()}
	if s.StartCommand != "" {
		c.set(c.entries[s.ID], "stopped", "requested")
	}
	return nil
}
func (c *Controller) set(e *managedEntry, state, reason string) {
	e.state.State = state
	e.state.Reason = reason
	if c.changed != nil {
		c.changed(e.state)
	}
}
func (c *Controller) State(id string) ModelState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[id]; e != nil {
		return e.state
	}
	return ModelState{}
}

// Adopt never launches: a stopped model remains stopped at application startup.
func (c *Controller) Adopt(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[id]
	if e == nil {
		return nil
	}
	p, err := c.find(e.spec)
	if err != nil {
		c.set(e, "failed", "ownership")
		return err
	}
	if p != nil {
		e.process = p
		e.last = c.now()
		c.set(e, "loading", "starting")
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		err := c.health(ctx, e.spec)
		cancel()
		if err == nil {
			c.set(e, "running", "ready")
		}
	}
	return nil
}
func (c *Controller) Replace(s Spec) error {
	c.mu.Lock()
	old := c.entries[s.ID]
	var previous Spec
	if old != nil {
		previous = old.spec
	}
	c.mu.Unlock()
	if c.State(s.ID).ConnectionID != "" {
		if err := c.Stop(s.ID, "requested"); err != nil {
			return err
		}
		c.mu.Lock()
		delete(c.entries, s.ID)
		c.mu.Unlock()
	}
	if err := c.Configure(s); err != nil {
		return err
	}
	if err := c.Adopt(s.ID); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, p := range s.Files {
		keep[p] = true
	}
	for _, p := range previous.Files {
		if !keep[p] && insideRoot(previous.Root, p) {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
func (c *Controller) States() []ModelState {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ModelState, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.state)
	}
	return out
}
func (c *Controller) Start(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	e := c.entries[id]
	if e == nil {
		c.mu.Unlock()
		return errors.New("connection is not managed")
	}
	if e.removing {
		c.mu.Unlock()
		return errors.New("model is being removed")
	}
	if e.done != nil {
		done := e.done
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			if c.State(id).State == "running" {
				return nil
			}
			return errors.New("model did not start")
		}
	}
	alive := e.process != nil && !e.process.exited()
	if alive && e.state.State == "running" {
		c.mu.Unlock()
		return nil
	}
	startCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.done = make(chan struct{})
	done := e.done
	s := e.spec
	p := e.process
	if !alive {
		p = nil
	}
	c.set(e, "loading", "starting")
	c.mu.Unlock()
	defer cancel()
	var err error
	if p == nil {
		p, err = c.find(s)
	}
	reason := "ownership"
	if err == nil && p == nil {
		p, err = c.launch(s)
		reason = "launch"
	}
	if err == nil {
		reason = "health"
		err = c.health(startCtx, s)
	}
	if err != nil && p != nil {
		if stopErr := p.stop(); stopErr == nil {
			p = nil
		} else {
			err = errors.Join(err, stopErr)
			reason = "ownership"
		}
	}
	c.mu.Lock()
	e.process = p
	e.cancel = nil
	e.done = nil
	e.last = c.now()
	if err != nil {
		c.set(e, "failed", reason)
	} else {
		c.set(e, "running", "ready")
	}
	close(done)
	c.mu.Unlock()
	return err
}

// BeginStart reserves at most one background start per configured connection.
// Returning promptly lets the paired phone stop a model while it loads.
func (c *Controller) BeginStart(id string) error {
	c.mu.Lock()
	e := c.entries[id]
	if e == nil || e.removing {
		c.mu.Unlock()
		return errors.New("connection is not managed")
	}
	if e.asyncDone != nil || e.done != nil || (e.process != nil && !e.process.exited() && e.state.State == "running") {
		c.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	e.asyncDone = done
	e.asyncCancel = cancel
	c.set(e, "loading", "starting")
	c.mu.Unlock()
	go func() {
		_ = c.Start(ctx, id)
		cancel()
		c.mu.Lock()
		e.asyncDone = nil
		e.asyncCancel = nil
		close(done)
		c.mu.Unlock()
	}()
	return nil
}
func (c *Controller) Stop(id, reason string) error {
	c.mu.Lock()
	e := c.entries[id]
	if e == nil {
		c.mu.Unlock()
		return errors.New("connection is not managed")
	}
	if e.asyncDone != nil {
		e.asyncCancel()
		done := e.asyncDone
		c.mu.Unlock()
		<-done
		return c.Stop(id, reason)
	}
	if e.cancel != nil {
		e.cancel()
		done := e.done
		c.mu.Unlock()
		<-done
		return c.Stop(id, reason)
	}
	if e.process != nil {
		if err := e.stopProcess(); err != nil {
			if errors.Is(err, ErrStopCommandRequired) {
				c.mu.Unlock()
				return err
			}
			c.set(e, "failed", "ownership")
			c.mu.Unlock()
			return err
		}
		e.process = nil
	}
	c.set(e, "stopped", reason)
	c.mu.Unlock()
	return nil
}

var ErrStopCommandRequired = errors.New("Save a stop command for a server started outside this app")

func commandReady(ctx context.Context, s Spec) bool {
	client := &http.Client{Timeout: 250 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(s.URL, "/")
	for _, endpoint := range []string{strings.TrimSuffix(base, "/v1") + "/health", base + "/models", strings.TrimSuffix(base, "/v1") + "/v1/models"} {
		r, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			continue
		}
		if s.APIKey != "" {
			r.Header.Set("Authorization", "Bearer "+s.APIKey)
		}
		response, err := client.Do(r)
		if err == nil {
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return true
			}
		}
	}
	return false
}

func findCommand(s Spec) (*managedProcess, error) {
	if !commandReady(context.Background(), s) {
		return nil, nil
	}
	stop := func(command string) error {
		if command == "" {
			return ErrStopCommandRequired
		}
		copy := s
		copy.StopCommand = command
		if err := runStopCommand(copy); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for commandReady(ctx, s) {
			select {
			case <-ctx.Done():
				return errors.New("server still answers after stop command")
			case <-time.After(100 * time.Millisecond):
			}
		}
		return ctx.Err()
	}
	return &managedProcess{exited: func() bool { return !commandReady(context.Background(), s) }, stop: func() error { return stop(s.StopCommand) }, commandStop: stop}, nil
}

func (c *Controller) Acquire(ctx context.Context, id string) (func(), error) {
	c.mu.Lock()
	e := c.entries[id]
	if e == nil {
		c.mu.Unlock()
		return func() {}, nil
	}
	e.active++
	c.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { c.mu.Lock(); e.active--; e.last = c.now(); c.mu.Unlock() }) }
	if err := c.Start(ctx, id); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
func (c *Controller) Tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.process == nil || e.done != nil {
			continue
		}
		if e.process.exited() {
			e.process = nil
			c.set(e, "stopped", "exited")
			continue
		}
		if e.state.State == "loading" {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			err := c.health(ctx, e.spec)
			cancel()
			if err == nil {
				c.set(e, "running", "ready")
			} else if c.now().Sub(e.last) >= 60*time.Second {
				if e.stopProcess() == nil {
					e.process = nil
				}
				c.set(e, "failed", "health")
			}
			continue
		}
		if e.active == 0 && e.spec.IdleMinutes > 0 && c.now().Sub(e.last)/time.Minute >= time.Duration(e.spec.IdleMinutes) {
			if e.stopProcess() == nil {
				e.process = nil
				c.set(e, "stopped", "idle")
			}
		}
	}
}
func (c *Controller) Close() {
	c.once.Do(func() {
		close(c.quit)
		for _, s := range c.States() {
			_ = c.Stop(s.ConnectionID, "requested")
		}
	})
}
func (c *Controller) Remove(id string) error {
	c.mu.Lock()
	e := c.entries[id]
	if e == nil {
		c.mu.Unlock()
		return errors.New("connection is not managed")
	}
	e.removing = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); e.removing = false; c.mu.Unlock() }()
	if err := c.Stop(id, "requested"); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range e.spec.Files {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}
		if !insideRoot(e.spec.Root, p) {
			return errors.New("installed file ownership could not be proved")
		}
	}
	for _, p := range e.spec.Files {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, name := range []string{"managed-model.json", "start-model.cmd"} {
		if err := os.Remove(filepath.Join(e.spec.Root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
func (c *Controller) Forget(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[id]; e != nil && e.process == nil && e.done == nil {
		delete(c.entries, id)
	}
}

// Removing command configuration relinquishes control; it does not stop an
// already running server. In-flight starts finish cancellation before removal.
func (c *Controller) UnconfigureCommand(id string) {
	c.mu.Lock()
	e := c.entries[id]
	if e == nil || e.spec.StartCommand == "" {
		c.mu.Unlock()
		return
	}
	if e.asyncDone != nil {
		e.asyncCancel()
		done := e.asyncDone
		c.mu.Unlock()
		<-done
		c.UnconfigureCommand(id)
		return
	}
	if e.done != nil {
		e.cancel()
		done := e.done
		c.mu.Unlock()
		<-done
		c.UnconfigureCommand(id)
		return
	}
	if e.process != nil && e.process.release != nil {
		e.process.release()
	}
	delete(c.entries, id)
	c.mu.Unlock()
}
func insideRoot(root, path string) bool {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func legacyLauncher(command string) []byte {
	return []byte(`CreateObject("WScript.Shell").Run """` + strings.ReplaceAll(command, `"`, `""`) + `""", 0, False` + "\r\n")
}
func removeLegacyLauncher(root, startup string) error {
	command := filepath.Join(root, "start-model.cmd")
	if _, err := os.Stat(command); err != nil {
		return nil
	}
	if startup == "" {
		return nil
	}
	path := filepath.Join(startup, "Agent_b-model.vbs")
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(body, legacyLauncher(command)) {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	log.Print("removed installed model sign-in launcher")
	return nil
}
func SaveSpec(s Spec) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Root, "managed-model.json"), b, 0600)
}
func LoadSpec(root, startup, url string) (Spec, error) {
	var s Spec
	b, err := os.ReadFile(filepath.Join(root, "managed-model.json"))
	if err == nil {
		err = json.Unmarshal(b, &s)
	} else if os.IsNotExist(err) {
		b, err = os.ReadFile(filepath.Join(root, "start-model.cmd"))
		if err != nil {
			return s, err
		}
		parts := regexp.MustCompile(`"((?:[^"]|"")*)"`).FindAllStringSubmatch(string(b), -1)
		if len(parts) < 2 {
			return s, errors.New("saved model command is invalid")
		}
		s = Spec{ID: "installed-local", Root: root, Executable: parts[0][1], URL: url, Label: "Installed model", IdleMinutes: 60}
		for _, p := range parts[1:] {
			s.Args = append(s.Args, strings.ReplaceAll(p[1], `""`, `"`))
		}
		for i, arg := range s.Args {
			if arg == "-m" && i+1 < len(s.Args) {
				model := s.Args[i+1]
				s.Label = filepath.Base(model)
				s.Files = append(s.Files, model, filepath.Join(filepath.Dir(model), "context-sizing.json"))
			}
		}
		rel, _ := filepath.Rel(root, s.Executable)
		pieces := strings.Split(rel, string(filepath.Separator))
		if len(pieces) >= 4 && pieces[0] == "llama.cpp" {
			dir := filepath.Join(root, pieces[0], pieces[1], pieces[2])
			archives, _ := filepath.Glob(filepath.Join(dir, "*.zip"))
			for _, path := range archives {
				z, err := zip.OpenReader(path)
				if err != nil {
					return s, err
				}
				s.Files = append(s.Files, path)
				for _, f := range z.File {
					if !f.FileInfo().IsDir() {
						p := filepath.Join(dir, filepath.FromSlash(f.Name))
						if !insideRoot(root, p) {
							z.Close()
							return s, errors.New("installed archive ownership could not be proved")
						}
						s.Files = append(s.Files, p)
					}
				}
				z.Close()
			}
		}
		for _, path := range s.Files {
			if info, err := os.Stat(path); err == nil {
				s.Bytes += info.Size()
			}
		}
	}
	if err != nil {
		return s, err
	}
	s.Root = root
	if !insideRoot(root, s.Executable) {
		return s, errors.New("installed executable ownership could not be proved")
	}
	if err = removeLegacyLauncher(root, startup); err != nil {
		return s, err
	}
	return s, nil
}
