// Command agentb runs one task in the current directory from a prompt.
//
// Item 2iz. It is the ENGINE without the app: the same configuration, the same
// connections and credentials, the same tools and the same jail, and none of
// the web server, the window, reflection, the updater, signing or
// notifications. That last claim is asserted by a test over `go list -deps`
// rather than trusted, because an import added in six months' time would
// otherwise undo it silently.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"harness/internal/agent"
	"harness/internal/buildinfo"
	"harness/internal/cli"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
	"harness/internal/tools"
)

func main() {
	options, err := cli.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if options.Help {
		fmt.Println(cli.Usage)
		return
	}
	if options.Version {
		info := buildinfo.Current()
		fmt.Printf("agentb %s %s\n", info.Tag, info.Display)
		return
	}
	if code := run(options); code != 0 {
		os.Exit(code)
	}
}

func run(options cli.Options) int {
	workspace, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	// Item 2iz (e): the run is journaled beside the work, in the folder's own
	// .agentb/, in the journal's schema — so the app's replay and `--replay`
	// read one file rather than two formats.
	local := filepath.Join(workspace, ".agentb")
	if err := os.MkdirAll(local, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	cfg, err := loadConfig(options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 6
	}

	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(local, "logs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer writers.Close()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)

	if options.Replay {
		return replay(local)
	}

	connections := func(id string) (*config.Connection, bool) {
		for index := range cfg.Connections {
			if cfg.Connections[index].ID == id {
				return &cfg.Connections[index], true
			}
		}
		return nil, false
	}
	snapshot := func() config.Config { return cfg }

	registry := session.NewRegistry(bus, writers, connections, cfg.Run.MaxTurns, snapshot)
	toolRegistry, err := buildTools(cfg, bus, registry, workspace, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	prompt, err := agent.LoadTemplate(promptPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "the system prompt could not be read: %v\n", err)
		return 2
	}

	runner := agent.NewRunner(bus, toolRegistry, prompt, connections, snapshot)
	item, err := registry.Create("agentb", cfg.DefaultAgentID(), workspace)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// The terminal is the approver, unless nothing may ask.
	channel, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	approver := cli.NewApprover(os.Stdin, os.Stderr, runner.Gate().Decide)
	renderer := cli.NewRenderer(os.Stdout, os.Stderr, options)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	runID := "r1"
	started := time.Now()
	item.Append(events.Message{Role: "user", Content: options.Task})

	stream := make(chan events.Event, 256)
	go func() {
		defer close(stream)
		for event := range channel {
			if !options.Unattended && approver.Handle(event) {
				continue
			}
			stream <- event
			if event.Type == events.RunStopped {
				return
			}
		}
	}()

	go func() {
		reason, detail, _ := runner.Run(ctx, item, runID)
		_ = reason
		_ = detail
	}()

	reason, detail := renderer.Follow(stream, runID)
	if !options.JSON {
		fmt.Fprintln(os.Stderr, cli.StopLine(reason, detail, time.Since(started)))
	}
	return cli.ExitCode(reason)
}

// loadConfig reads the same harness.json the app reads, so a profile, a
// connection and a credential mean the same thing at a prompt as in the window.
func loadConfig(options cli.Options) (config.Config, error) {
	path, err := configPath()
	if err != nil {
		return config.Config{}, err
	}
	loaded, _, _, err := config.Load(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	cfg := *loaded
	if err := config.ResolveConnectionCredentials(&cfg, filepath.Dir(path)); err != nil {
		return cfg, err
	}
	// Item 5f: unattended is the existing rule, reached from a flag rather than
	// reimplemented.
	if options.Unattended {
		cfg.Approval.Unattended = true
	}
	if options.Profile != "" {
		if !pointAgentsAt(&cfg, options.Profile) {
			return cfg, fmt.Errorf("no connection named %q; agentb --help", options.Profile)
		}
	}
	return cfg, nil
}

// pointAgentsAt re-points the b role at the named connection for this
// invocation only. Nothing is saved: a one-shot task does not rewrite the
// operator's configuration.
func pointAgentsAt(cfg *config.Config, label string) bool {
	found := false
	for _, connection := range cfg.Connections {
		if connection.ID == label || connection.Label == label {
			label = connection.ID
			found = true
			break
		}
	}
	if !found {
		return false
	}
	for index := range cfg.Agents {
		cfg.Agents[index].B = label
	}
	return true
}

func configPath() (string, error) {
	if explicit := os.Getenv("AGENTB_CONFIG"); explicit != "" {
		return explicit, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Agent_b", "harness.json"), nil
}

func promptPath() string {
	if explicit := os.Getenv("AGENTB_PROMPTS"); explicit != "" {
		return filepath.Join(explicit, "system.md")
	}
	executable, err := os.Executable()
	if err != nil {
		return filepath.Join("prompts", "system.md")
	}
	return filepath.Join(filepath.Dir(executable), "prompts", "system.md")
}

func buildTools(cfg config.Config, bus *events.Bus, registry *session.Registry, workspace string, options cli.Options) (*tools.Registry, error) {
	credentials := credential.New(filepath.Dir(mustConfigPath()))
	workspaces := session.NewWorkspaceRegistry()
	coordinator := tools.NewFileCoordinator(workspaces, registry.Label, bus)
	fileIdentity := tools.NewFileIdentity(credentials)
	shellTool := tools.NewShell(cfg.Shell)
	// Item 2iz (e): the memory layers are READ-ONLY unless --memory is given. A
	// one-shot task in somebody's repository should not quietly change what the
	// agent remembers everywhere else -- so without the flag the remember tool is
	// simply not registered, which is an absence rather than a flag a later
	// change could forget to read.
	memoryManager := memory.New(filepath.Dir(mustConfigPath()), func() config.Config { return cfg }, nil)
	rememberTool := tools.Tool(tools.NewRemember(memoryManager, bus))
	if !options.Memory {
		rememberTool = readOnlyRemember{}
	}
	fetchTool := tools.NewFetch(cfg.Tools.Fetch)
	return tools.New(
		fileIdentity.Wrap(tools.NewReadFile(cfg.Tools.ReadFile)),
		fileIdentity.Wrap(tools.NewListDir(cfg.Tools.ListDir)),
		fileIdentity.Wrap(tools.NewWriteFile(coordinator)),
		fileIdentity.Wrap(tools.NewEditFile(coordinator)),
		tools.NewSearch(
			fileIdentity.Wrap(tools.NewGrep(cfg.Tools.Grep, cfg.Tools.ListDir)),
			fileIdentity.Wrap(tools.NewGlob(cfg.Tools.FindFiles)),
		),
		shellTool,
		rememberTool,
		tools.NewRecall(memoryManager),
		fetchTool,
		tools.NewWebSearch(fetchTool, cfg.Tools.WebSearch),
		tools.NewRunScript(shellTool),
		tools.NewCallService(cfg.Services),
		tools.NewDelegate(),
	), nil
}

// readOnlyRemember stands in for the remember tool when --memory was not given.
// It keeps the registration order stable -- the thirteen tools are a contract --
// while refusing the write with the reason, which is more useful to a model than
// a tool that is simply missing.
type readOnlyRemember struct{}

func (readOnlyRemember) Name() string { return "remember" }
func (readOnlyRemember) Description() string {
	return "Disabled in this run: agentb loads the memory layers read-only unless --memory is given."
}
func (readOnlyRemember) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (readOnlyRemember) Call(context.Context, *session.Session, map[string]any) (string, error) {
	return "error: memory is read-only in this run; re-run with --memory to allow writes", nil
}

func mustConfigPath() string {
	path, err := configPath()
	if err != nil {
		return ""
	}
	return path
}

// replay prints this folder's journal. It reads the same JSONL the app reads,
// which is the point of writing it there.
func replay(local string) int {
	dir := filepath.Join(local, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "no run has been recorded in this folder yet\n")
		return 1
	}
	printed := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		os.Stdout.Write(body)
		printed = true
	}
	if !printed {
		fmt.Fprintln(os.Stderr, "no run has been recorded in this folder yet")
		return 1
	}
	return 0
}
