package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness/internal/projection"
)

// Item 2mx: THE SPOKEN LINE INVENTS NOTHING.
//
// A voice assistant has no screen, so whatever it says IS the product's whole report.
// Every branch below comes from something that already happened, and the last one is
// the one that matters most: when nothing has happened, it says so rather than
// describing a run it cannot see.
func TestTheSpokenLineComesFromWhatHappenedAndNothingElse(t *testing.T) {
	for _, one := range []struct {
		name      string
		pending   *spokenApproval
		lastReply string
		stopped   string
		running   bool
		want      string
	}{
		{
			name:    "an approval that is waiting is the only thing worth saying",
			pending: &spokenApproval{Summary: "run a shell command: git status"},
			// Deliberately with a reply present: nothing else will happen until the
			// approval is answered, so the approval wins.
			lastReply: "I looked at the repository.",
			want:      "Waiting for your approval: run a shell command: git status",
		},
		{
			name:    "an approval with no summary still says what kind of thing it is",
			pending: &spokenApproval{},
			want:    "Waiting for your approval: a tool call",
		},
		{
			name:      "the reply is the answer",
			lastReply: "There are three files changed.",
			want:      "There are three files changed.",
		},
		{
			name:    "a run that stopped says why",
			stopped: "max_turns",
			want:    "The run stopped: max_turns",
		},
		{
			name:    "a run still going says so, without guessing at an outcome",
			running: true,
			want:    "Still working.",
		},
		{
			name: "nothing to report is said plainly",
			want: "Nothing to report yet.",
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			got := spokenLine(one.pending, one.lastReply, one.stopped, one.running)
			if got != one.want {
				t.Errorf("spoken = %q, want %q", got, one.want)
			}
			// Whatever it says, it is ONE line: a spoken sentence with a newline in it
			// is two sentences by the time it is heard.
			if strings.Contains(got, "\n") {
				t.Errorf("the spoken line carries a newline: %q", got)
			}
		})
	}
}

// The last reply is the newest COMPLETED one, from the run asked about. A reply still
// being written is not an answer yet.
func TestTheLastReplyIsTheNewestCompletedOneFromThatRun(t *testing.T) {
	entries := []projection.ChatEntry{
		{Type: "assistant", RunID: "r1", Text: "an older answer", Done: true},
		{Type: "user", RunID: "r2", Text: "a question"},
		{Type: "assistant", RunID: "r2", Text: "the answer for r2", Done: true},
		{Type: "assistant", RunID: "r2", Text: "still being written", Done: false},
	}
	if got := lastAssistantReply(entries, "r2"); got != "the answer for r2" {
		t.Errorf("reply = %q", got)
	}
	if got := lastAssistantReply(entries, "r1"); got != "an older answer" {
		t.Errorf("reply for the older run = %q", got)
	}
	if got := lastAssistantReply(entries, "r9"); got != "" {
		t.Errorf("a run with no reply produced %q", got)
	}
	// A user message is never spoken back as if it were an answer.
	if got := lastAssistantReply([]projection.ChatEntry{{Type: "user", RunID: "r3", Text: "my own words", Done: true}}, "r3"); got != "" {
		t.Errorf("a user message was taken as a reply: %q", got)
	}
}

// Item 2mx: A SUBMIT THAT CAN BE REPEATED. A phone retries; a retry must not start a
// second run. The same key gets the same answer back.
func TestARepeatedSubmissionGetsTheFirstAnswerAndDoesNotRunTwice(t *testing.T) {
	store := newIdempotentSubmissions()
	if _, found := store.remembered("k1"); found {
		t.Fatal("an unknown key was remembered")
	}
	store.remember("k1", 202, map[string]any{"run_id": "r1"})
	reply, found := store.remembered("k1")
	if !found || reply.status != 202 {
		t.Fatalf("the first answer was not kept: %+v %v", reply, found)
	}
	if body, _ := reply.body.(map[string]any); body["run_id"] != "r1" {
		t.Errorf("a different answer came back: %+v", reply.body)
	}
	// An empty key is not a key: a caller that sends none gets no sharing at all,
	// because two unrelated requests must never be treated as the same one.
	store.remember("", 202, map[string]any{"run_id": "r2"})
	if _, found := store.remembered(""); found {
		t.Error("an empty key was treated as a key")
	}
}

func TestARememberedSubmissionExpiresAndTheStoreStaysBounded(t *testing.T) {
	store := newIdempotentSubmissions()
	store.remember("old", 202, "first")
	// Reach in and age it past the window rather than waiting ten minutes.
	store.mu.Lock()
	store.replies["old"] = idempotentReply{status: 202, body: "first", at: time.Now().Add(-idempotentWindow - time.Minute)}
	store.mu.Unlock()
	if _, found := store.remembered("old"); found {
		t.Error("an expired key was still honoured")
	}

	for index := 0; index < idempotentLimit+50; index++ {
		store.remember(string(rune('a'+index%26))+strings.Repeat("x", index), 202, index)
	}
	store.mu.Lock()
	size := len(store.replies)
	store.mu.Unlock()
	if size > idempotentLimit {
		t.Errorf("the store grew to %d, past its bound of %d", size, idempotentLimit)
	}
}

// The route answers only its own shape, and says so rather than guessing.
func TestTheBriefRouteRefusesWhatIsNotABriefRequest(t *testing.T) {
	server := &Server{}
	for _, path := range []string{"/api/runs/", "/api/runs/r1", "/api/runs/r1/other", "/api/runs//brief"} {
		recorder := httptest.NewRecorder()
		server.runBrief(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != 404 {
			t.Errorf("%s answered %d", path, recorder.Code)
		}
	}
	// A POST to the right shape is still not a read.
	recorder := httptest.NewRecorder()
	server.runBrief(recorder, httptest.NewRequest("POST", "/api/runs/r1/brief", nil))
	if recorder.Code != 404 {
		t.Errorf("a POST answered %d", recorder.Code)
	}
}
