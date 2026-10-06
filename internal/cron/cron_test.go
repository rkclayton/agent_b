package cron

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 5, 8, 0, 0, 0, time.Local) // Monday

func TestScheduleForms2qs(t *testing.T) {
	tests := []struct {
		in     string
		want   time.Time
		repeat bool
	}{
		{"in 30m", base.Add(30 * time.Minute), false}, {"in 2h", base.Add(2 * time.Hour), false}, {"in 1d", base.Add(24 * time.Hour), false},
		{"2026-10-05T12:00:00-05:00", time.Date(2026, 10, 5, 12, 0, 0, 0, time.FixedZone("", -5*3600)), false},
		{"every 30m", base.Add(30 * time.Minute), true}, {"every 2h", base.Add(2 * time.Hour), true}, {"every 1d", base.Add(24 * time.Hour), true}, {"30m", base.Add(30 * time.Minute), true},
		{"every day at 9am", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local), true}, {"daily at 7am", time.Date(2026, 10, 6, 7, 0, 0, 0, time.Local), true},
		{"weekdays at 9am", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local), true}, {"weekends at 10am", time.Date(2026, 10, 10, 10, 0, 0, 0, time.Local), true},
		{"every monday 9am", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local), true}, {"0 9 * * 1-5", time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local), true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			spec, err := Parse(tc.in, base)
			if err != nil {
				t.Fatal(err)
			}
			if !spec.Next.Equal(tc.want) || spec.Repeating != tc.repeat {
				t.Fatalf("got %v repeat=%v", spec.Next, spec.Repeating)
			}
		})
	}
	if _, err := Parse("whenever", base); err == nil || !strings.Contains(err.Error(), "in 30m") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("malformed schedule error = %v", err)
	}
}

func TestActionsNamesAndAtomicStore2qs(t *testing.T) {
	now := base
	m := New(t.TempDir(), func() time.Time { return now }, nil)
	created, err := m.Apply(context.Background(), Args{Action: "create", Schedule: "every 2h", Prompt: "check X", Name: "price", Skills: []string{"one", "two"}, Deliver: "origin"})
	if err != nil || created.Job.Next.IsZero() {
		t.Fatalf("create = %+v %v", created, err)
	}
	if _, err := os.Stat(filepath.Join(m.Root(), "jobs.json")); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.Apply(context.Background(), Args{Action: "list"}); len(list.Jobs) != 1 {
		t.Fatalf("list=%+v", list)
	}
	if _, err = m.Apply(context.Background(), Args{Action: "update", JobID: "price", Schedule: "every 1d", Prompt: "changed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Apply(context.Background(), Args{Action: "pause", JobID: "price"}); err != nil || !m.Jobs()[0].Paused {
		t.Fatalf("pause: %v %+v", err, m.Jobs())
	}
	if _, err = m.Apply(context.Background(), Args{Action: "resume", JobID: "price"}); err != nil || m.Jobs()[0].Paused {
		t.Fatalf("resume: %v", err)
	}
	_, _ = m.Apply(context.Background(), Args{Action: "create", Schedule: "in 1h", Prompt: "x", Name: "same"})
	_, _ = m.Apply(context.Background(), Args{Action: "create", Schedule: "in 2h", Prompt: "x", Name: "same"})
	if _, err = m.Apply(context.Background(), Args{Action: "pause", JobID: "same"}); err == nil || !strings.Contains(err.Error(), "candidates") {
		t.Fatalf("ambiguous=%v", err)
	}
	if _, err = m.Apply(context.Background(), Args{Action: "remove", JobID: "price"}); err != nil || len(m.Jobs()) != 2 {
		t.Fatalf("remove: %v %+v", err, m.Jobs())
	}
}

func TestTickMissedSilentFailureDedupeAndRepeat2qs(t *testing.T) {
	now := base
	runs, notices, retained := []string{}, []string{}, []string{}
	m := New(t.TempDir(), func() time.Time { return now }, func(_ context.Context, j Job) Result {
		runs = append(runs, j.Name)
		switch j.Prompt {
		case "quiet":
			return Result{Answer: "nothing [SILENT]"}
		case "bad":
			return Result{Failed: true, Failure: "same failure", Answer: "[SILENT]"}
		default:
			return Result{Answer: "news"}
		}
	})
	m.SetHooks(func(name string) { notices = append(notices, name) }, func(j Job, _ Result, keep bool) {
		if keep {
			retained = append(retained, j.Name)
		}
	})
	_, _ = m.Apply(context.Background(), Args{Action: "create", Name: "quiet", Schedule: "every 30m", Prompt: "quiet"})
	_, _ = m.Apply(context.Background(), Args{Action: "create", Name: "loud", Schedule: "every 30m", Prompt: "loud", Repeat: 2})
	_, _ = m.Apply(context.Background(), Args{Action: "create", Name: "bad", Schedule: "every 30m", Prompt: "bad"})
	_, _ = m.Apply(context.Background(), Args{Action: "create", Name: "paused", Schedule: "every 30m", Prompt: "loud"})
	_, _ = m.Apply(context.Background(), Args{Action: "pause", JobID: "paused"})
	now = now.Add(95 * time.Minute)
	if err := m.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(runs, ",") != "quiet,loud,bad" || strings.Join(notices, ",") != "loud,bad" || strings.Join(retained, ",") != "loud,bad" {
		t.Fatalf("runs=%v notices=%v retained=%v", runs, notices, retained)
	}
	now = now.Add(2 * time.Hour)
	_ = m.Tick(context.Background())
	if count(notices, "bad") != 1 {
		t.Fatalf("repeated failure notices=%v", notices)
	}
	if has(m.Jobs(), "loud") {
		t.Fatalf("repeat=2 job remains: %+v", m.Jobs())
	}
	files, _ := filepath.Glob(filepath.Join(m.Root(), "output", "*", "*.md"))
	if len(files) < 6 {
		t.Fatalf("output files=%d", len(files))
	}
}

func TestRunNowAndOneShotRemoval2qs(t *testing.T) {
	now := base
	ran := 0
	m := New(t.TempDir(), func() time.Time { return now }, func(context.Context, Job) Result { ran++; return Result{Answer: "ok"} })
	_, _ = m.Apply(context.Background(), Args{Action: "create", Name: "once", Schedule: "in 1d", Prompt: "x"})
	if _, err := m.Apply(context.Background(), Args{Action: "run", JobID: "once"}); err != nil || ran != 1 || has(m.Jobs(), "once") {
		t.Fatalf("run now: ran=%d jobs=%+v err=%v", ran, m.Jobs(), err)
	}
}

func TestFullBounds2qs(t *testing.T) {
	m := New(t.TempDir(), func() time.Time { return base }, func(context.Context, Job) Result { return Result{Answer: "bounded"} })
	for i := 0; i < maxJobs; i++ {
		m.jobs = append(m.jobs, Job{ID: fmt.Sprintf("%04d", i), Name: "job", Schedule: "every 1d", Prompt: "x", Next: base.Add(time.Hour), Repeating: true})
	}
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(m.Root(), "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.jobs = []Job{{ID: "output", Name: "output", Schedule: "every 1d", Prompt: "x", Next: base, Repeating: true}}
	for i := 0; i < maxOutputsPerJob+12; i++ {
		m.now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		if err := m.runOne(context.Background(), m.jobs[0], false); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(m.Root(), "output", "output", "*.md"))
	if len(files) != maxOutputsPerJob {
		t.Fatalf("outputs=%d", len(files))
	}
	t.Logf("2qs full bound: %d jobs=%d bytes; outputs retained=%d", maxJobs, info.Size(), len(files))
}

func count(v []string, s string) int {
	n := 0
	for _, x := range v {
		if x == s {
			n++
		}
	}
	return n
}
func has(v []Job, s string) bool {
	for _, x := range v {
		if x.Name == s {
			return true
		}
	}
	return false
}
