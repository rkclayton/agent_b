package agent

import (
	"testing"

	"harness/internal/events"
	"harness/internal/session"
)

// rel-1.23.0 card 5: A QUEUED MESSAGE KEEPS ITS PLACE ACROSS A RESTART.
//
// The card measured it exactly: QueueUserAttachments writes the turn to the journal
// before it is queued, so the MESSAGE always survived — but `s.pending` is a map in
// this process, so its POSITION did not. A restart turned a queue of three into
// three ordinary history messages that nothing would ever send.
func TestAQueuedMessageKeepsItsPlaceAcrossARestart(t *testing.T) {
	item := &session.Session{ID: "s1", Workspace: t.TempDir()}
	item.Append(events.Message{ID: "m-1", Role: "user", Category: "history", Turn: 1, Content: "first"})
	item.Append(events.Message{ID: "m-2", Role: "user", Category: "history", Turn: 2, Content: "second"})
	item.Append(events.Message{ID: "m-3", Role: "user", Category: "history", Turn: 3, Content: "third"})
	// What the journal said when the process ended: two of the three were queued.
	item.SetQueuedMessageIDs([]string{"m-2", "m-3"})

	scheduler := &Scheduler{pending: map[string][]queuedRun{}, held: map[string]bool{}, unreachable: map[string]bool{}, active: map[string]*activeRun{}}
	if count := scheduler.RestoreQueue(item); count != 2 {
		t.Fatalf("restored %d queued message(s), want 2", count)
	}
	queue := scheduler.pending["s1"]
	if len(queue) != 2 || queue[0].userMessageID != "m-2" || queue[1].userMessageID != "m-3" {
		t.Fatalf("the queue came back out of order: %+v", queue)
	}
	if queue[0].userMessage.Content != "second" {
		t.Fatalf("the queued entry lost its message: %+v", queue[0].userMessage)
	}
	// Held, not running: the queue existed because something was in the way, and a
	// restart is not evidence that it cleared.
	if !scheduler.held["s1"] {
		t.Fatal("the restored queue was not held")
	}
	if run := item.Snapshot().Run; run.Status != "held" || run.QueuePosition != 2 {
		t.Fatalf("run state=%+v, want held at position 2", run)
	}

	// A queue entry whose message is no longer in the conversation is dropped rather
	// than carried as something that would send nothing.
	other := &session.Session{ID: "s2", Workspace: t.TempDir()}
	other.Append(events.Message{ID: "m-9", Role: "user", Category: "history", Turn: 1, Content: "still here"})
	other.SetQueuedMessageIDs([]string{"m-8", "m-9"})
	if count := scheduler.RestoreQueue(other); count != 1 {
		t.Fatalf("restored %d, want only the message that is still there", count)
	}
	// And a chat with nothing queued is untouched.
	empty := &session.Session{ID: "s3", Workspace: t.TempDir()}
	if count := scheduler.RestoreQueue(empty); count != 0 || scheduler.held["s3"] {
		t.Fatalf("a chat with no queue was changed: count=%d held=%v", count, scheduler.held["s3"])
	}
}
