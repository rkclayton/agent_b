package web

import (
	"testing"

	"harness/internal/events"
)

func TestScheduledPhoneNoticeCarriesAnswer(t *testing.T) {
	data := events.WithHuman(events.RunStopped, map[string]any{
		"reason": "done", "scheduled_job": "page-watch release", "scheduled_answer": "version changed\nhttps://example.invalid/release",
	})
	delete(data, "scheduled_answer")
	event := events.New(events.RunStopped, "chat", "run", data)
	want := "Scheduled job page-watch release finished.\nversion changed\nhttps://example.invalid/release"
	if got := pushNotice(event); got != want {
		t.Fatalf("notice=%q want=%q", got, want)
	}
}
