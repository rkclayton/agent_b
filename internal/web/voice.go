package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"net/url"
	"regexp"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/projection"
)

// Item 2mx: SIRI, AND WHAT A VOICE ASSISTANT ACTUALLY NEEDS.
//
// A Shortcut cannot read a screen, cannot retry safely and cannot be told "look at the
// settings sheet". Three things follow, and they are the whole of this file:
//
//   - A SUBMIT THAT CAN BE REPEATED. Siri retries on a flaky network, and a retried
//     "add milk to the list" that starts a second run is worse than one that fails. The
//     key rides in a HEADER, so `/api/message`'s body shape does not change for anyone.
//   - SOMETHING TO SAY BACK. `spoken` is one short line drawn from what already
//     happened: the last reply, the approval that is waiting, or the reason the run
//     stopped. Nothing is invented, and when there is nothing to say it says that.
//   - ONE CHAT TO LAND IN. A spoken request names no chat, so the server keeps one and
//     creates it once.
//
// The listener stays on loopback. Reachability from a phone is `tailscale serve`, which
// is the operator's to configure and is written down in docs/SIRI.md — the product never
// binds anywhere else.
//
// One credential is enough: W0 confirmed the phone bearer short-circuits both the
// mutation-token guard and the browser-session guard, so a Shortcut carries the bearer
// and nothing else.

// idempotentWindow is how long a submission key is remembered. Long enough to cover a
// phone's retries on a bad connection, short enough that it is not a second store.
const idempotentWindow = 10 * time.Minute

// idempotentLimit bounds the store, because it is fed by a public route.
const idempotentLimit = 256

type idempotentReply struct {
	status int
	body   any
	at     time.Time
	// Item 2mx (b): a retry with the same key AND THE SAME SESSION replays. The session
	// is part of the identity because a key reused against a different chat is a
	// different request, and replaying the first chat's answer into the second would be
	// a wrong answer rather than a safe retry.
	session string
}

type idempotentSubmissions struct {
	mu      sync.Mutex
	replies map[string]idempotentReply
}

func newIdempotentSubmissions() *idempotentSubmissions {
	return &idempotentSubmissions{replies: map[string]idempotentReply{}}
}

// remembered returns a previous reply for this key, if one is still in the window.
func (i *idempotentSubmissions) remembered(key, session string) (idempotentReply, bool) {
	if key == "" {
		return idempotentReply{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	reply, found := i.replies[key]
	if !found {
		return idempotentReply{}, false
	}
	if time.Since(reply.at) > idempotentWindow {
		delete(i.replies, key)
		return idempotentReply{}, false
	}
	// A key reused against another chat is not this request.
	if reply.session != session {
		return idempotentReply{}, false
	}
	return reply, true
}

// remember stores a reply under a key, dropping what has expired and, if the store is
// still full, the oldest entry.
func (i *idempotentSubmissions) remember(key, session string, status int, body any) {
	if key == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for existing, reply := range i.replies {
		if time.Since(reply.at) > idempotentWindow {
			delete(i.replies, existing)
		}
	}
	// Evict until there is room, seeding the search from an ACTUAL entry. Seeding it
	// from time.Now() looked right and evicted nothing: the clock's resolution is coarse
	// enough that entries written in the same millisecond are not Before(now), so on a
	// burst the store sailed past its bound. A test caught it at 281 of 256.
	for len(i.replies) >= idempotentLimit {
		oldest, at, seeded := "", time.Time{}, false
		for existing, reply := range i.replies {
			if !seeded || reply.at.Before(at) {
				oldest, at, seeded = existing, reply.at, true
			}
		}
		if oldest == "" {
			break
		}
		delete(i.replies, oldest)
	}
	i.replies[key] = idempotentReply{status: status, body: body, at: time.Now(), session: session}
}

// idempotencyKey is the header a caller sends to make a submission repeatable. A key
// longer than this is refused rather than stored, because the store is public-facing.
const idempotencyKeyMax = 200

func idempotencyKeyOf(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}

// voiceSessionID resolves the chat a spoken request lands in: the configured one when it
// still exists, otherwise a chat labelled Siri, created ONCE and written to the
// configuration so the next request finds it rather than making another.
func (s *Server) voiceSessionID() (string, error) {
	configured := strings.TrimSpace(s.ConfigSnapshot().Voice.DefaultSessionID)
	if configured != "" {
		if _, ok := s.registry.Get(configured); ok {
			return configured, nil
		}
	}
	// The label is the operator's word for it, and it is how he will recognise the chat
	// in his own list. A spoken request cannot choose an agent, and a chat with no agent
	// is refused, so the installation's default answers for it.
	agentID := ""
	settings := s.ConfigSnapshot()
	// (d): THE DEFAULT agent, not merely the first one in the file. The installation
	// already has an answer to which agent is the default and a spoken request should land
	// on the same one a new chat would.
	if agent, ok := settings.Agent(settings.DefaultAgentID()); ok {
		agentID = agent.B
		if strings.TrimSpace(agentID) == "" {
			agentID = config.AgentID(agent.Name)
		}
	}
	if strings.TrimSpace(agentID) == "" && len(settings.Agents) > 0 {
		agentID = settings.Agents[0].B
	}
	if strings.TrimSpace(agentID) == "" && len(settings.Connections) > 0 {
		agentID = settings.Connections[0].ID
	}
	if strings.TrimSpace(agentID) == "" {
		return "", errors.New("this installation has no connection to send a spoken request to; add one in Settings first")
	}
	created, err := s.registry.Create("Siri", agentID, "")
	if err != nil {
		return "", err
	}
	id := created.Snapshot().ID
	s.mu.Lock()
	next := *s.cfg
	next.Voice.DefaultSessionID = id
	// voice is PROFILE-scoped: a session belongs to a profile, so this is written where
	// that scope lives and a profile switch cannot send a spoken request into another
	// profile's chat.
	err = s.saveProfileConfig(next)
	if err == nil {
		s.cfg.Voice.DefaultSessionID = id
	}
	s.mu.Unlock()
	return id, err
}

// spokenLine is item 2mx's brief: ONE SHORT LINE, drawn from what already happened and
// nothing else.
//
// The order of precedence is the order of urgency. An approval that is waiting is the
// only thing worth saying, because nothing else will happen until it is answered. A run
// that stopped for a reason is next, because the reason is the outcome. Otherwise the
// last reply is the answer. If none of those exist there is nothing to say, and saying
// so is better than a cheerful sentence about a run that did nothing.
func spokenLine(pending *spokenApproval, lastReply, stopReason string, running bool) string {
	if pending != nil {
		what := strings.TrimSpace(pending.Summary)
		if what == "" {
			what = "run a tool"
		}
		return "Agent_b needs your approval to " + what
	}
	if reply := strings.TrimSpace(lastReply); reply != "" {
		return reply
	}
	if reason := strings.TrimSpace(stopReason); reason != "" {
		return reason
	}
	if running {
		return "Still working."
	}
	return "Nothing to report yet."
}

// spokenApproval is the part of a pending approval a spoken line needs.
type spokenApproval struct {
	Summary string `json:"summary"`
	CallID  string `json:"call_id,omitempty"`
}

// Item 2mx (c): THE STATE VOCABULARY IS THE ITEM'S, NOT THE RUN'S.
//
// The brief passed the run's own status through, which is the harness's word for it.
// (c) names six states a caller can branch on without knowing how the harness talks:
// queued, running, needs_approval, done, failed, stopped. An approval outranks the run
// status, because nothing else will happen until it is answered.
func briefState(status string, pending bool, stopReason string) string {
	if pending {
		return "needs_approval"
	}
	switch status {
	case "queued":
		return "queued"
	case "running", "stopping":
		return "running"
	}
	switch strings.TrimSpace(stopReason) {
	case "":
		return "done"
	case "done":
		return "done"
	case "stopped", "operator_stopped", "canceled", "cancelled":
		return "stopped"
	}
	return "failed"
}

// spokenReplyLimit is (c)'s STATED LENGTH. A spoken sentence is heard, not read: past
// this a listener has lost the beginning by the time the end arrives, and a Shortcut
// reading a whole answer aloud was the thing to avoid.
const spokenReplyLimit = 360

// plainSpoken strips the markdown a reply is written in, because a listener hears
// asterisks and backticks as nothing at all while they eat the length budget. It removes
// the marks and keeps the words; it is not a markdown parser and does not try to be.
func plainSpoken(text string) string {
	replaced := strings.NewReplacer(
		"```", " ", "`", "", "**", "", "__", "", "*", "", "_", "", "#", "", ">", "", "~~", "",
	).Replace(text)
	// Link syntax keeps the words and drops the target: the target cannot be spoken.
	replaced = markdownLink.ReplaceAllString(replaced, "$1")
	// A LIST BULLET IS NOT A WORD, and it is only a bullet at the start of a line: a
	// hyphen inside non-trivial must survive, so this is done per line rather than by
	// replacing every dash. A test caught "- " reaching the spoken line.
	lines := strings.Split(strings.ReplaceAll(replaced, "\r\n", "\n"), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, bullet := range []string{"- ", "+ ", "• "} {
			trimmed = strings.TrimPrefix(trimmed, bullet)
		}
		lines[index] = trimmed
	}
	replaced = strings.Join(lines, " ")
	// Any run of whitespace, including the newlines a reply is full of, becomes one space.
	replaced = strings.Join(strings.Fields(replaced), " ")
	if len(replaced) <= spokenReplyLimit {
		return replaced
	}
	// Cut on a word boundary and say that it was cut, rather than stopping mid-word as
	// though the connection dropped.
	cut := replaced[:spokenReplyLimit]
	if space := strings.LastIndex(cut, " "); space > spokenReplyLimit/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, " ,;:") + "… (the rest is in the chat)"
}

var markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// humanStopReason is (c)'s `human.happened`: the stop reason in the words the product
// already uses for it everywhere else, rather than the identifier. "connection_not_runnable"
// is not something to say out loud.
func humanStopReason(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	notice := events.HumanNoticeFor(events.RunStopped, map[string]any{"reason": reason})
	if strings.TrimSpace(notice.Happened) != "" {
		return notice.Happened
	}
	return reason
}

// runBrief is item 2mx's `GET /api/runs/{id}/brief`: one line a voice assistant can
// speak about a run, and the structured state behind it for anything that wants more.
//
// It INVENTS NOTHING. Every word of `spoken` comes from the approval that is waiting,
// the reply that was written or the reason the run stopped — and when none of those
// exist it says there is nothing to report rather than describing a run that did not
// happen.
func (s *Server) runBrief(w http.ResponseWriter, r *http.Request) {
	tail := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
	runID, action, found := strings.Cut(tail, "/")
	if r.Method != http.MethodGet || !found || action != "brief" || strings.TrimSpace(runID) == "" {
		http.NotFound(w, r)
		return
	}

	// The run is found by asking the sessions which of them owns it: a run id is not a
	// key anywhere, and a voice caller has only the id the submission gave it.
	snapshot, ok := s.sessionOwningRun(runID)
	if !ok {
		writeError(w, http.StatusNotFound, "no run with that id, on this installation", "run_id")
		return
	}

	var pending *spokenApproval
	if snapshot.PendingApproval != nil {
		pending = &spokenApproval{Summary: approvalSummaryOf(snapshot.PendingApproval), CallID: snapshot.PendingApproval.CallID}
	}
	lastReply := lastAssistantReply(snapshot.Chat, runID)
	running := snapshot.Run.Status == "running" || snapshot.Run.Status == "queued" || snapshot.Run.Status == "stopping"
	stopped := ""
	if !running {
		stopped = snapshot.Run.LastStopReason
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":     runID,
		"session_id": snapshot.ID,
		"chat":       snapshot.Label,
		"status":     snapshot.Run.Status,
		// (c): the state a caller branches on, in the item's own six words, and where to
		// go to answer an approval — a voice assistant cannot show a screen, so it hands
		// over a link instead.
		"state":    briefState(snapshot.Run.Status, pending != nil, stopped),
		"chat_url": "/chat?session=" + url.QueryEscape(snapshot.ID),
		// The three fields the line is drawn from, so a caller that wants to phrase it
		// differently has the same material and does not have to parse the sentence.
		"pending_approval": pending,
		"last_reply":       lastReply,
		"last_stop_reason": snapshot.Run.LastStopReason,
		"spoken":           spokenLine(pending, plainSpoken(lastReply), humanStopReason(stopped), running),
	})
}

// sessionOwningRun finds the projected chat that ran a run, by its current run or the
// last one it finished.
func (s *Server) sessionOwningRun(runID string) (projection.Snapshot, bool) {
	sessions, err := s.projectedSessions()
	if err != nil {
		return projection.Snapshot{}, false
	}
	for _, snapshot := range sessions {
		if snapshot.Run.RunID == runID || snapshot.Run.LastRunID == runID {
			return snapshot, true
		}
	}
	// A run whose chat has since been closed is still answerable from its entries.
	for _, snapshot := range sessions {
		for _, entry := range snapshot.Chat {
			if entry.RunID == runID {
				return snapshot, true
			}
		}
	}
	return projection.Snapshot{}, false
}

// projectedSessions is the same projection the state endpoint serves.
func (s *Server) projectedSessions() (map[string]projection.Snapshot, error) {
	if s.projector == nil || s.writers == nil {
		return nil, errors.New("this installation keeps no projection")
	}
	return s.projector.Snapshot(s.writers.SessionCursors())
}

// lastAssistantReply is the newest completed answer in a run, as text. A reply still
// being written is not an answer yet, so it is not spoken.
func lastAssistantReply(entries []projection.ChatEntry, runID string) string {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.Type != "assistant" || (runID != "" && entry.RunID != runID) {
			continue
		}
		if !entry.Done {
			continue
		}
		if text := strings.TrimSpace(entry.Text); text != "" {
			return text
		}
	}
	return ""
}

// approvalSummaryOf is the approval's own words. The projector already carries the human
// summary the console shows; this speaks the same one rather than composing a second.
func approvalSummaryOf(entry *projection.ChatEntry) string {
	if entry == nil {
		return ""
	}
	if text := strings.TrimSpace(entry.Text); text != "" {
		return text
	}
	return strings.TrimSpace(entry.Name)
}
