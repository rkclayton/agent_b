package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/memory"
	"harness/internal/session"
)

// Item 2jf: remember rarely, scoped, replacing, within a budget.
//
// The tool used to take a note and a target and append. Four things are wrong
// with that and this fixes them at the tool rather than in the prompt, because
// a rule the prompt asks for is a rule the model may or may not follow, and a
// rule the tool enforces is a rule:
//
//   - an UNSCOPED note is refused: "user", "repository" or "environment" is the
//     model saying what kind of fact this is, and it decides which layer holds
//     it rather than a default doing so silently;
//   - a note may REPLACE one it names, in the same write, so superseding a
//     belief is one action instead of an append and a stale line;
//   - TWO WRITES PER RUN, enforced here. The third is refused with what to do
//     instead;
//   - the layer has a BUDGET on the file. Item 2eb trimmed on the way in and
//     asked the model to consolidate, so the file grew forever and nobody met
//     the limit. The write is refused now and the harness never trims.
type Remember struct {
	memory *memory.Manager
	bus    *events.Bus
	cfg    func() config.Config
	mu     sync.Mutex
	// writes counts this run's writes, per session and run. (c) is a per-RUN
	// cap, so the key is both.
	writes map[string]int
}

func NewRemember(manager *memory.Manager, bus *events.Bus) *Remember {
	return &Remember{memory: manager, bus: bus, writes: map[string]int{}}
}

// SetConfig gives the tool the layer budget. Without it the budget is the
// package default, which is what a caller that does not configure gets.
func (r *Remember) SetConfig(cfg func() config.Config) { r.cfg = cfg }

func (*Remember) Name() string { return "remember" }
func (*Remember) Description() string {
	return "Save one durable fact a future chat will need. Call recall first. Say its scope: user (about the user), repository (about this project), or environment (about this machine). If it supersedes a note, pass that note's text as replaces and it is removed in the same write. Two notes, plus one per 25 turns (six maximum), per run; a full layer refuses the write. Never save command output, tool results, transient state, or anything already recorded by this chat."
}
func (*Remember) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"note":     map[string]any{"type": "string"},
			"scope":    map[string]any{"type": "string", "enum": []string{"user", "repository", "environment"}},
			"replaces": map[string]any{"type": "string"},
		},
		"required": []string{"note", "scope"},
	}
}

const MaxWritesPerRun = 6

// layerFor is (b)'s routing: user and environment are about the user and
// the machine, so they follow the agent everywhere; repository is about this
// project, so it follows the folder. A repository note with no folder in scope
// falls to the agent layer, which is item 2fh's existing rule and is kept.
func layerFor(scope string) string {
	if scope == "repository" {
		return "folder"
	}
	return "agent"
}

func (r *Remember) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	note, _ := args["note"].(string)
	if strings.TrimSpace(note) == "" {
		return "", fmt.Errorf("note is empty")
	}
	if memory.AboutAgentNote(note) {
		return "error: remember refused: notes about the agent's own tools are not kept; the user decides what the agent may do", nil
	}
	// (b): an unscoped note is REFUSED. The scope is the model saying what kind
	// of fact this is, and guessing it for them is how the folder layer filled
	// up with facts about the operator.
	scope := strings.ToLower(strings.TrimSpace(stringValue(args["scope"], "")))
	switch scope {
	case "user", "repository", "environment":
	case "":
		return "error: scope is required: user (about the user), repository (about this project), or environment (about this machine)", nil
	default:
		return fmt.Sprintf("error: scope %q is not one of user, repository or environment", scope), nil
	}

	// (c): two writes per run, counted here rather than asked for in the prompt.
	key := s.ID + "\x00" + s.Run.LastRunID
	r.mu.Lock()
	written := r.writes[key]
	r.mu.Unlock()
	allowance := 2 + s.Run.Turn/25
	if allowance > MaxWritesPerRun {
		allowance = MaxWritesPerRun
	}
	if written >= allowance {
		return "", fmt.Errorf("%d notes this run; replace one or skip", written)
	}

	target, fell := layerFor(scope), ""
	path := ""
	if target == "folder" {
		// Item 2fh, kept: a scratch chat has no folder layer. A project fact with
		// no project in scope goes to the agent layer, and says so.
		if folder := s.MemoryFolder(); folder != "" {
			path = r.memory.Path(folder)
		} else {
			target = "agent"
			fell = " No project was in scope, so it went to the agent layer, which every chat of this agent loads."
		}
	}
	if target == "agent" {
		path = r.memory.AgentPath(s.AgentID)
	}
	if target == "folder" {
		if root := memory.RepoRoot(s.MemoryFolder()); root != "" {
			if err := memory.EnsureRepoNotesIgnored(root); err != nil {
				return "", err
			}
		}
	}

	write := memory.Write{
		Note:            note,
		Scope:           scope,
		Replaces:        stringValue(args["replaces"], ""),
		Run:             s.Run.LastRunID,
		Chat:            s.ID,
		Turn:            s.Run.Turn,
		UntrustedInTurn: s.UntrustedInTurn(),
		Budget:          r.budget(),
	}
	duplicate, err := r.memory.WriteNote(path, write)
	if err != nil {
		// (d) and (b): a full layer and a missing replaces are both answers the
		// model can act on, not harness failures. They come back as tool errors
		// with the instruction in them rather than as an error the run stops on.
		if errors.Is(err, memory.ErrMemoryFull) || errors.Is(err, memory.ErrNoteNotFound) {
			return "", err
		}
		return "", err
	}
	if duplicate {
		return "ok: already noted" + fell, nil
	}
	r.mu.Lock()
	r.writes[key] = written + 1
	r.mu.Unlock()
	replaced := ""
	if strings.TrimSpace(write.Replaces) != "" {
		replaced = " Replaced: " + firstWords(write.Replaces, 8) + "."
	}
	r.bus.Publish(events.New(events.MemoryNoted, s.ID, s.Run.LastRunID, map[string]any{
		"note": note, "path": path, "target": target, "agent_id": s.AgentID,
		// (e): the transcript row shows scope and whether it replaced.
		"scope": scope, "replaced": strings.TrimSpace(write.Replaces) != "", "replaced_note": firstWords(write.Replaces, 8),
		"untrusted_in_turn": write.UntrustedInTurn,
	}))
	return fmt.Sprintf("ok: noted as %s; active next session.%s%s", scope, replaced, fell), nil
}

func firstWords(value string, count int) string {
	words := strings.Fields(value)
	if len(words) > count {
		words = words[:count]
	}
	return strings.Join(words, " ")
}

// budget is the per-layer file budget. It is the same number the injection has
// always used, now enforced on the file instead of by trimming on the way in.
func (r *Remember) budget() int {
	if r.cfg == nil {
		return 0
	}
	return r.cfg().Memory.MaxTokens
}
