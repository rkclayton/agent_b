package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness/internal/agent"
	"harness/internal/chatstore"
	"harness/internal/config"
	"harness/internal/credential"
	"harness/internal/cron"
	"harness/internal/events"
	"harness/internal/session"
	"harness/internal/tools"
)

type recordedHostWindowActions struct{ action func(string, string) bool }

func scheduledFixture(t *testing.T, modelURL string) (*scheduledRuns, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	cfg.Approval.Mode, cfg.Context.Accounting = config.ApprovalModeOff, "estimated"
	cfg.Connections[0].BaseURL = modelURL
	cfg.Connections[0].Model = "fixture"
	cfg.Connections[0].Context.NCtx = 32768
	cfg.Connections[0].Capabilities.ToolCalls = true
	cfg.Connections[0].Capabilities.Streaming = true
	connection := func(id string) (*config.Connection, bool) { return cfg.Connection(id) }
	settings := func() config.Config { return cfg }
	bus := events.NewBus()
	writers, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writers.Close() })
	registry := session.NewRegistry(bus, writers, connection, 0, settings)
	registry.SetPlansRoot(filepath.Join(root, "plans"))
	promptPath := filepath.Join(root, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("Work on the test fixture. {{tools}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt, err := agent.LoadTemplate(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(bus, tools.New(tools.NewShell(cfg.Shell)), prompt, connection, settings)
	return &scheduledRuns{profile: root, cfg: settings, registry: registry, scheduler: agent.NewScheduler(runner, registry, bus, settings), bus: bus}, root
}

func TestScheduledModelWaitAndShutdownReplay2zj(t *testing.T) {
	for _, scenario := range []string{"loads-after-25-minutes", "never-answers", "application-closes"} {
		t.Run(scenario, func(t *testing.T) {
			entered, answer, finish := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/v1/chat/completions" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-req.Context().Done():
					return
				case <-finish:
					return
				case <-answer:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Fixture answered.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer model.Close()
			defer close(finish)
			runs, root := scheduledFixture(t, model.URL)
			first, reset := make(chan time.Duration, 1), make(chan time.Duration, 1)
			ticks := make(chan time.Time, 1)
			runs.timer = func(duration time.Duration) scheduledAlarm {
				first <- duration
				return scheduledAlarm{expired: ticks, reset: func(d time.Duration) bool { reset <- d; return true }, stop: func() bool { return true }}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan cron.Result, 1)
			go func() { result <- runs.run(ctx, cron.Job{Name: "fixture job", Prompt: "Use the fixture model."}) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("scheduled model never received the request")
			}
			limit := <-first
			if limit != 30*time.Minute {
				t.Fatalf("model wait timer=%v", limit)
			}
			advance := func(elapsed time.Duration) {
				if elapsed >= limit {
					ticks <- time.Unix(0, 0).Add(elapsed)
				}
			}
			switch scenario {
			case "loads-after-25-minutes":
				advance(25 * time.Minute)
				close(answer)
			case "never-answers":
				advance(30 * time.Minute)
			default:
				cancel()
			}
			select {
			case got := <-result:
				switch scenario {
				case "loads-after-25-minutes":
					if got.Failed || got.Answer != "Fixture answered." || len(reset) != 1 || <-reset != 20*time.Minute {
						t.Fatalf("late load result=%+v", got)
					}
				case "never-answers":
					if !got.Failed || !got.Retryable || !strings.Contains(got.Failure, "model_unreachable") {
						t.Fatalf("unavailable result=%+v", got)
					}
				default:
					item, ok := runs.registry.Get(got.ChatID)
					if !got.Interrupted || !ok || item.Snapshot().Run.LastStopReason != "cron_shutdown" {
						t.Fatalf("shutdown result=%+v", got)
					}
					now := time.Now()
					clock := func() time.Time { return now }
					manager := cron.New(root, clock, func(context.Context, cron.Job) cron.Result { return got })
					created, err := manager.Apply(context.Background(), cron.Args{Action: "create", Schedule: "in 1h", Prompt: "fixture"})
					if err != nil {
						t.Fatal(err)
					}
					now = created.Job.Next
					if err := manager.Tick(context.Background()); err != nil {
						t.Fatal(err)
					}
					calls := 0
					manager = cron.New(root, clock, func(context.Context, cron.Job) cron.Result { calls++; return cron.Result{Answer: "ready"} })
					for i := 0; i < 2; i++ {
						if err := manager.Tick(context.Background()); err != nil {
							t.Fatal(err)
						}
					}
					if calls != 1 || len(manager.Jobs()) != 0 {
						t.Fatalf("restart calls=%d remaining=%d", calls, len(manager.Jobs()))
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("scheduled run did not reach its result")
			}
		})
	}
}

func TestScheduledFinalFailurePublishesOneNotice2zj(t *testing.T) {
	now, attempts := time.Now(), 0
	runs := &scheduledRuns{bus: events.NewBus()}
	stream, unsubscribe := runs.bus.Subscribe()
	defer unsubscribe()
	manager := cron.New(t.TempDir(), func() time.Time { return now }, func(context.Context, cron.Job) cron.Result {
		attempts++
		return cron.Result{Failed: true, Retryable: true, Failure: "model unavailable", Answer: "fixture reply"}
	})
	manager.SetHooks(nil, runs.finish)
	created, err := manager.Apply(context.Background(), cron.Args{Action: "create", Name: "fixture job", Schedule: "every 2h", Deliver: "origin", Prompt: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	now = created.Job.Next
	for index, delay := range []time.Duration{0, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute} {
		now = now.Add(delay)
		if err := manager.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if attempts != index+1 {
			t.Fatalf("after %v, attempts=%d", delay, attempts)
		}
		if index < 3 && len(stream) != 0 {
			t.Fatalf("notified after attempt %d", attempts)
		}
	}
	if len(stream) != 1 {
		t.Fatalf("final notices=%d", len(stream))
	}
	item := <-stream
	data, _ := item.Data.(map[string]any)
	if item.Type != events.ScheduledFinished || data["scheduled_job"] != "fixture job" || data["reason"] != "failed" || data["scheduled_answer"] != nil {
		t.Fatalf("notice=%+v", item)
	}
}

func TestScheduledShellStartsInTheCollisionResolvedFolder2zj(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil && runtime.GOOS != "windows" {
		t.Skip("PowerShell is required for the shell fixture")
	}
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"shell-fixture","type":"function","function":{"name":"shell","arguments":"{\"command\":\"Set-Content -LiteralPath fixture-output -Value ready\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Fixture answered.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()
	runs, root := scheduledFixture(t, model.URL)
	parent := filepath.Join(root, "chats", "Scheduled")
	original, err := chatstore.New(parent).Create("main", "fixture job", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := runs.run(ctx, cron.Job{Name: "fixture job", Prompt: "Use the shell on the test fixture."})
	item, ok := runs.registry.Get(got.ChatID)
	if got.Failed || !ok || filepath.Dir(item.Snapshot().Workspace) != parent || item.Snapshot().Workspace == original {
		t.Fatalf("schedule=%+v", got)
	}
	result, err := os.ReadFile(filepath.Join(item.Snapshot().Workspace, "fixture-output"))
	if err != nil || strings.TrimSpace(string(result)) != "ready" {
		t.Fatalf("shell fixture=%q err=%v", result, err)
	}
}

func TestAnUnchangedInvalidChatIsReportedOnce2zf(t *testing.T) {
	logs := filepath.Join(t.TempDir(), "logs")
	cfg := config.Defaults(t.TempDir())
	var journal string
	seed := func(created string) []byte {
		event := events.New(events.SessionCreated, "s1", "", map[string]any{"session": map[string]any{"id": "s1", "agent_id": cfg.DefaultAgentID(), "created_at": created, "run": map[string]any{"status": "idle"}}})
		event.Raw = "old diagnostic data"
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		return append(body, '\n')
	}
	original := seed("")
	for start := 0; start < 4; start++ {
		writers, err := events.NewWriters(logs)
		if err != nil {
			t.Fatal(err)
		}
		journal = filepath.Join(writers.ChatDir(), "s1.jsonl")
		if start == 0 || start == 3 {
			body := original
			if start == 3 {
				body = seed("2026-10-02T12:00:00Z")
			}
			if err := os.WriteFile(journal, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		bus := events.NewBus()
		failures := 0
		bus.SetDurableSink(func(event events.Event) (events.LogCursor, error) {
			if event.Type == events.SessionRestoreFailed {
				failures++
			}
			return writers.WriteRecord(event)
		}, nil, nil)
		registry := session.NewRegistry(bus, writers, func(id string) (*config.Connection, bool) { return cfg.Connection(id) }, 40, func() config.Config { return cfg })
		chats, _, err := restoreRetainedChats(writers, registry, bus, 0)
		if err != nil || (failures == 1) != (start == 0) || failures > 1 || (len(chats) == 1) != (start == 3) {
			t.Fatalf("start %d: chats=%d failures=%d err=%v", start, len(chats), failures, err)
		}
		if err := writers.Close(); err != nil {
			t.Fatal(err)
		}
		if start < 3 {
			got, err := os.ReadFile(journal)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("start %d changed the unreadable journal: %v", start, err)
			}
		}
	}
}

func (r *recordedHostWindowActions) SetHostWindowAction(action func(string, string) bool) {
	r.action = action
}

func TestBackgroundLaunchesReceiveLaterWindowActions2t2(t *testing.T) {
	for _, launch := range []string{"installer restart: -Detached -NoBrowser -NoPause", "sign-in start: -AtLogon -Detached -NoBrowser -NoPause"} {
		t.Run(launch, func(t *testing.T) {
			server := &recordedHostWindowActions{}
			attachHostWindowActions(server)
			if server.action == nil {
				t.Fatal("a background-started server has no action for the window a later launch opens")
			}
		})
	}
}

func TestStartupDoesNotCreateAChatWithoutAnOperatorAction2qd(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), `registry.Create("main", mainAgentID, "")`) {
		t.Fatal("startup still creates a chat when no retained chat is open")
	}
}

func TestStartupRestoresMarkedChatAndKeepsArchiveMark2xt(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	logs := filepath.Join(root, "logs")
	writers, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults(workspace)
	connection := &cfg.Connections[0]
	bus := events.NewBus()
	bus.SetDurableSink(writers.WriteRecord, nil, nil)
	registry := session.NewRegistry(bus, writers, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	registry.SetPlansRoot(filepath.Join(root, "plans"))
	chat, err := registry.Create("kept name", cfg.DefaultAgentID(), "")
	if err != nil {
		t.Fatal(err)
	}
	chat.NamePinned = true
	bus.Publish(events.New(events.MessageAppended, chat.ID, "", map[string]any{"message": events.Message{ID: "m1", Role: "user", Category: "history", Content: "retained words"}}))
	meta, _ := chatstore.ReadMetadata(chat.Workspace)
	meta.ArchivedAt, meta.Pinned = time.Now(), true
	if err := chatstore.WriteMetadata(chat.Workspace, meta); err != nil {
		t.Fatal(err)
	}
	if err := writers.Close(); err != nil {
		t.Fatal(err)
	}

	nextWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	defer nextWriters.Close()
	nextBus := events.NewBus()
	nextRegistry := session.NewRegistry(nextBus, nextWriters, func(id string) (*config.Connection, bool) { return connection, id == connection.ID }, 40, func() config.Config { return cfg })
	nextRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	restored, _, err := restoreRetainedChats(nextWriters, nextRegistry, nextBus, 0)
	if err != nil || len(restored) != 1 || restored[0].Snapshot().Label != "kept name" {
		t.Fatalf("restore=%v err=%v", restored, err)
	}
	meta, err = chatstore.ReadMetadata(chat.Workspace)
	if err != nil || meta.ArchivedAt.IsZero() || !meta.Pinned {
		t.Fatalf("metadata=%+v err=%v", meta, err)
	}
}

func TestRetainedChatsRestoreWithoutOperationalLogsAndDeleteExplicitly(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	logs := filepath.Join(root, "logs")
	cfg := config.Defaults(workspace)
	connection := &cfg.Connections[0]
	connections := func(id string) (*config.Connection, bool) { return connection, id == connection.ID }

	firstWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	firstBus := events.NewBus()
	firstBus.SetDurableSink(firstWriters.WriteRecord, nil, nil)
	firstRegistry := session.NewRegistry(firstBus, firstWriters, connections, 40, func() config.Config { return cfg })
	firstRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	item, err := firstRegistry.Create("main", cfg.DefaultAgentID(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	message := events.Message{ID: "m1", Role: "user", Category: "history", Content: "retained words"}
	item.Append(message)
	firstBus.Publish(events.New(events.MessageAppended, item.ID, "", map[string]any{"message": message}))
	if err := firstRegistry.RenameBy(item.ID, "durable name", "c"); err != nil {
		t.Fatal(err)
	}
	other, err := firstRegistry.CreateLike(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	launchEvents, unsubscribe := firstBus.Subscribe()
	defer unsubscribe()
	publishServiceSetupInvitation(firstRegistry, firstBus)
	invitation := <-launchEvents
	data, _ := invitation.Data.(map[string]any)
	if invitation.Type != events.ServiceIdentityUnavailable || invitation.SessionID != other.ID || data["action"] != "provision" || data["launch"] != true {
		t.Fatalf("launch invitation=%+v", invitation)
	}
	otherMessage := events.Message{ID: "m2", Role: "user", Category: "history", Content: "independent context"}
	other.Append(otherMessage)
	firstBus.Publish(events.New(events.MessageAppended, other.ID, "", map[string]any{"message": otherMessage}))
	if err := firstWriters.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(logs); err != nil {
		t.Fatal(err)
	}

	secondWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondWriters.Close() })
	secondBus := events.NewBus()
	secondBus.SetDurableSink(secondWriters.WriteRecord, nil, nil)
	secondRegistry := session.NewRegistry(secondBus, secondWriters, connections, 40, func() config.Config { return cfg })
	restored, _, err := restoreRetainedChats(secondWriters, secondRegistry, secondBus, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored chats=%d", len(restored))
	}
	byID := map[string]session.Snapshot{}
	for _, restoredChat := range restored {
		byID[restoredChat.ID] = restoredChat.Snapshot()
	}
	if snapshot := byID[item.ID]; snapshot.Label != "durable name" || len(snapshot.Messages) != 1 || snapshot.Messages[0].Content != "retained words" {
		t.Fatalf("restored first snapshot=%+v", snapshot)
	}
	if snapshot := byID[other.ID]; len(snapshot.Messages) != 1 || snapshot.Messages[0].Content != "independent context" {
		t.Fatalf("restored second snapshot=%+v", snapshot)
	}
	for _, restoredChat := range restored {
		if err := secondRegistry.Close(restoredChat.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := secondRegistry.Delete(restoredChat.ID); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := secondWriters.DurableChatPaths()
	if err != nil || len(paths) != 0 {
		t.Fatalf("durable paths after explicit delete=%v err=%v", paths, err)
	}
}

func TestDuplicateAndOrphanedHelperDoNotBlockStartup2sc(t *testing.T) {
	root := t.TempDir()
	cfg := config.Defaults(filepath.Join(root, "workspace"))
	cfg.Agents = []config.Agent{{Name: "Primary", B: cfg.Connections[0].ID}, {Name: "Helper", B: cfg.Connections[0].ID}}
	connection := &cfg.Connections[0]
	resolve := func(id string) (*config.Connection, bool) { return connection, id == connection.ID }
	firstWriters, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	firstBus := events.NewBus()
	firstBus.SetDurableSink(firstWriters.WriteRecord, nil, nil)
	firstRegistry := session.NewRegistry(firstBus, firstWriters, resolve, 40, func() config.Config { return cfg })
	firstRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	primary, err := firstRegistry.Create("primary stand-in", config.AgentID("Primary"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = firstRegistry.Create("helper stand-in", config.AgentID("Helper"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err = chatstore.New(filepath.Join(root, "chats")).Create(primary.ID, "duplicate stand-in", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = firstWriters.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Agents = cfg.Agents[:1]
	secondWriters, err := events.NewWriters(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondWriters.Close() })
	secondBus := events.NewBus()
	secondBus.SetDurableSink(secondWriters.WriteRecord, nil, nil)
	secondRegistry := session.NewRegistry(secondBus, secondWriters, resolve, 40, func() config.Config { return cfg })
	secondRegistry.SetPlansRoot(filepath.Join(root, "plans"))
	oldWriter := log.Writer()
	defer log.SetOutput(oldWriter)
	var output bytes.Buffer
	log.SetOutput(&output)
	restored, _, err := restoreRetainedChats(secondWriters, secondRegistry, secondBus, 0)
	if err != nil || len(restored) != 1 || restored[0].ID != primary.ID {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	if !strings.Contains(output.String(), "removed 1 leftover folder") || !strings.Contains(output.String(), "unknown agent") || strings.Contains(output.String(), "duplicate chat id") {
		t.Fatalf("restore log did not name both dispositions: %s", output.String())
	}
}

func TestServiceIdentityStartupNoticeNamesConfiguredState(t *testing.T) {
	service := config.ShellServiceAccount{Enabled: true, Account: "agentb-svc"}
	if got := serviceIdentityStartupNotice(service, credential.Status{}); got != "service identity not set up: enabled=true; account=agentb-svc; credential=missing" {
		t.Fatalf("missing credential notice=%q", got)
	}
	if got := serviceIdentityStartupNotice(service, credential.Status{Stored: true}); got != "service identity not set up: enabled=true; account=agentb-svc; credential=stored" {
		t.Fatalf("stored credential notice=%q", got)
	}
}

func TestRestoreRetainedChatsBootstrapsNewestLegacyOperationalGeneration(t *testing.T) {
	root, workspace := t.TempDir(), t.TempDir()
	logs := filepath.Join(root, "logs")
	cfg := config.Defaults(workspace)
	connection := &cfg.Connections[0]
	connections := func(id string) (*config.Connection, bool) { return connection, id == connection.ID }
	firstWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	firstBus := events.NewBus()
	firstBus.SetDurableSink(firstWriters.WriteRecord, nil, nil)
	firstRegistry := session.NewRegistry(firstBus, firstWriters, connections, 40, func() config.Config { return cfg })
	item, err := firstRegistry.Create("upgrade chat", cfg.DefaultAgentID(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	message := events.Message{ID: "legacy", Role: "user", Category: "history", Content: "before upgrade"}
	item.Append(message)
	firstBus.Publish(events.New(events.MessageAppended, item.ID, "", map[string]any{"message": message}))
	if err := firstWriters.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "chats")); err != nil {
		t.Fatal(err)
	}

	secondWriters, err := events.NewWriters(logs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondWriters.Close() })
	secondBus := events.NewBus()
	secondBus.SetDurableSink(secondWriters.WriteRecord, nil, nil)
	secondRegistry := session.NewRegistry(secondBus, secondWriters, connections, 40, func() config.Config { return cfg })
	restored, _, err := restoreRetainedChats(secondWriters, secondRegistry, secondBus, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].Snapshot().Messages[0].Content != "before upgrade" {
		t.Fatalf("legacy restore=%+v", restored)
	}
	paths, err := secondWriters.DurableChatPaths()
	if err != nil || len(paths) != 1 {
		t.Fatalf("seeded durable paths=%v err=%v", paths, err)
	}
}

func TestReadServingFactsCompleteness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SERVING.md")
	if facts := readServingFacts(path); facts.Complete {
		t.Fatal("missing file reported complete")
	}
	if err := os.WriteFile(path, []byte("tokenize_idle_ms=8\ntokenize_blocks_on_slot=no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if facts := readServingFacts(path); facts.Complete {
		t.Fatal("partial file reported complete")
	}
	if err := os.WriteFile(path, []byte("tokenize_idle_ms=8\ntokenize_busy_ms=9\ntokenize_blocks_on_slot=no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	facts := readServingFacts(path)
	if !facts.Complete || facts.TokenizeIdleMS != 8 || facts.TokenizeBusyMS != 9 || facts.TokenizeBlocksOnSlot != "no" {
		t.Fatalf("unexpected facts: %+v", facts)
	}
}

func TestStartupElevationGuard(t *testing.T) {
	if err := startupElevationError(false); err != nil {
		t.Fatalf("standard token refused: %v", err)
	}
	err := startupElevationError(true)
	if err == nil || !strings.Contains(err.Error(), "refuses to run") || !strings.Contains(err.Error(), "File Explorer") {
		t.Fatalf("elevated token error = %v", err)
	}
}

func TestAllUsersInstallIsTheOnlyElevatedApplicationMode(t *testing.T) {
	for _, test := range []struct {
		executable string
		arguments  []string
		want       bool
	}{
		{`C:\download\Agent_b-setup.exe`, []string{"--all-users"}, true},
		{`C:\app\Agent_b.exe`, []string{"--install", "--all-users"}, true},
		{`C:\app\Agent_b.exe`, []string{"--all-users"}, false},
		{`C:\app\Agent_b.exe`, []string{"--install"}, false},
	} {
		if got := allUsersInstallRequested(test.executable, test.arguments); got != test.want {
			t.Fatalf("allUsersInstallRequested(%q, %v)=%t, want %t", test.executable, test.arguments, got, test.want)
		}
	}
}

func TestResolveStartupPathsPrecedence(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	localBase := t.TempDir()
	t.Setenv("LOCALAPPDATA", localBase)
	installedConfig := filepath.Join(localBase, "Agent_b", "harness.json")
	if err := os.MkdirAll(filepath.Dir(installedConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedConfig, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	environmentConfig := filepath.Join(t.TempDir(), "environment.json")
	explicitConfig := filepath.Join(t.TempDir(), "explicit.json")
	t.Setenv("AGENTB_CONFIG", environmentConfig)

	paths, err := resolveStartupPaths(explicitConfig, filepath.Join(cwd, "application"), filepath.Join(cwd, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != explicitConfig || paths.Data != filepath.Join(cwd, "data") || paths.Application != filepath.Join(cwd, "application") {
		t.Fatalf("explicit paths = %+v", paths)
	}

	paths, err = resolveStartupPaths("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != environmentConfig || paths.Data != filepath.Join(localBase, "Agent_b") {
		t.Fatalf("environment paths = %+v", paths)
	}

	t.Setenv("AGENTB_CONFIG", "")
	paths, err = resolveStartupPaths("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != installedConfig || paths.Data != filepath.Join(localBase, "Agent_b") {
		t.Fatalf("installed paths = %+v", paths)
	}

	if err := os.Remove(installedConfig); err != nil {
		t.Fatal(err)
	}
	paths, err = resolveStartupPaths("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Item 2gw (v1.2.5): the CONFIG and the DATA root still fall back to the
	// working directory in development - those are the operator's files. The
	// APPLICATION root does not: the binary's own web, prompts and scripts live
	// beside the binary, wherever it is run from.
	executable, execErr := os.Executable()
	if execErr != nil {
		t.Fatal(execErr)
	}
	wantApplication := filepath.Dir(executable)
	if resolved, linkErr := filepath.EvalSymlinks(executable); linkErr == nil {
		wantApplication = filepath.Dir(resolved)
	}
	if paths.Config != filepath.Join(cwd, "harness.json") || paths.Data != cwd || paths.Application != wantApplication {
		t.Fatalf("development paths = %+v", paths)
	}
}
