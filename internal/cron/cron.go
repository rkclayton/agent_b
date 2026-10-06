package cron

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const accepted = "accepted schedules: in 30m, in 2h, in 1d, an ISO timestamp, every 30m/2h/1d, a bare duration, daily/every day at 9am, weekdays/weekends at 9am, every monday 9am, or a five-field cron expression"
const maxJobs, maxOutputsPerJob = 1000, 128

type Spec struct {
	Next      time.Time
	Repeating bool
}

func Parse(raw string, now time.Time) (Spec, error) {
	trimmed := strings.TrimSpace(raw)
	s := strings.ToLower(trimmed)
	bad := func() (Spec, error) { return Spec{}, errors.New(accepted) }
	if strings.HasPrefix(s, "in ") {
		d, err := duration(strings.TrimSpace(s[3:]))
		if err != nil {
			return bad()
		}
		return Spec{Next: now.Add(d)}, nil
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		if !ts.After(now) {
			return bad()
		}
		return Spec{Next: ts}, nil
	}
	for _, prefix := range []string{"every ", ""} {
		if strings.HasPrefix(s, prefix) {
			if d, err := duration(strings.TrimSpace(strings.TrimPrefix(s, prefix))); err == nil {
				return Spec{Next: now.Add(d), Repeating: true}, nil
			}
		}
	}
	if strings.HasPrefix(s, "every day at ") {
		if h, m, ok := clock(s[len("every day at "):]); ok {
			return Spec{Next: nextClock(now, h, m, nil), Repeating: true}, nil
		}
	}
	if strings.HasPrefix(s, "daily at ") {
		if h, m, ok := clock(s[len("daily at "):]); ok {
			return Spec{Next: nextClock(now, h, m, nil), Repeating: true}, nil
		}
	}
	if strings.HasPrefix(s, "weekdays at ") {
		if h, m, ok := clock(s[len("weekdays at "):]); ok {
			days := map[time.Weekday]bool{time.Monday: true, time.Tuesday: true, time.Wednesday: true, time.Thursday: true, time.Friday: true}
			return Spec{Next: nextClock(now, h, m, days), Repeating: true}, nil
		}
	}
	if strings.HasPrefix(s, "weekends at ") {
		if h, m, ok := clock(s[len("weekends at "):]); ok {
			return Spec{Next: nextClock(now, h, m, map[time.Weekday]bool{time.Saturday: true, time.Sunday: true}), Repeating: true}, nil
		}
	}
	weekdays := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for i, name := range weekdays {
		p := "every " + name + " "
		if strings.HasPrefix(s, p) {
			if h, m, ok := clock(s[len(p):]); ok {
				return Spec{Next: nextClock(now, h, m, map[time.Weekday]bool{time.Weekday(i): true}), Repeating: true}, nil
			}
		}
	}
	if fields := strings.Fields(s); len(fields) == 5 {
		if next, ok := nextCron(fields, now); ok {
			return Spec{Next: next, Repeating: true}, nil
		}
	}
	return bad()
}

func duration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, errors.New("duration")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, errors.New("duration")
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return 0, errors.New("duration")
}
func clock(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	pm := strings.HasSuffix(s, "pm")
	am := strings.HasSuffix(s, "am")
	if pm || am {
		s = strings.TrimSpace(s[:len(s)-2])
	}
	parts := strings.Split(s, ":")
	h, e := strconv.Atoi(parts[0])
	if e != nil {
		return 0, 0, false
	}
	m := 0
	if len(parts) == 2 {
		m, e = strconv.Atoi(parts[1])
		if e != nil {
			return 0, 0, false
		}
	} else if len(parts) != 1 {
		return 0, 0, false
	}
	if am || pm {
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		if h == 12 {
			h = 0
		}
		if pm {
			h += 12
		}
	}
	return h, m, h < 24 && m < 60
}
func nextClock(now time.Time, h, m int, days map[time.Weekday]bool) time.Time {
	for n := 0; n < 8; n++ {
		d := now.AddDate(0, 0, n)
		candidate := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, now.Location())
		if candidate.After(now) && (days == nil || days[candidate.Weekday()]) {
			return candidate
		}
	}
	return time.Time{}
}
func nextCron(fields []string, now time.Time) (time.Time, bool) {
	for i, limits := range [][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}} {
		if !fieldValid(fields[i], limits[0], limits[1]) {
			return time.Time{}, false
		}
	}
	for n := 1; n <= 366*24*60; n++ {
		t := now.Truncate(time.Minute).Add(time.Duration(n) * time.Minute)
		if field(fields[0], t.Minute(), 0, 59) && field(fields[1], t.Hour(), 0, 23) && field(fields[2], t.Day(), 1, 31) && field(fields[3], int(t.Month()), 1, 12) && field(fields[4], int(t.Weekday()), 0, 6) {
			return t, true
		}
	}
	return time.Time{}, false
}
func fieldValid(expr string, min, max int) bool {
	for _, part := range strings.Split(expr, ",") {
		if part == "*" {
			continue
		}
		p := strings.Split(part, "-")
		if len(p) > 2 {
			return false
		}
		for _, v := range p {
			n, e := strconv.Atoi(v)
			if e != nil || n < min || n > max {
				return false
			}
		}
		if len(p) == 2 {
			a, _ := strconv.Atoi(p[0])
			b, _ := strconv.Atoi(p[1])
			if a > b {
				return false
			}
		}
	}
	return true
}
func field(expr string, value, min, max int) bool {
	for _, part := range strings.Split(expr, ",") {
		if part == "*" {
			return true
		}
		lo, hi := 0, 0
		if strings.Contains(part, "-") {
			p := strings.SplitN(part, "-", 2)
			lo, _ = strconv.Atoi(p[0])
			hi, _ = strconv.Atoi(p[1])
		} else {
			lo, _ = strconv.Atoi(part)
			hi = lo
		}
		if lo >= min && hi <= max && value >= lo && value <= hi {
			return true
		}
	}
	return false
}

type Job struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Schedule    string    `json:"schedule"`
	Prompt      string    `json:"prompt"`
	Skills      []string  `json:"skills,omitempty"`
	Deliver     string    `json:"deliver,omitempty"`
	Repeat      int       `json:"repeat,omitempty"`
	Runs        int       `json:"runs"`
	Next        time.Time `json:"next_run"`
	Paused      bool      `json:"paused,omitempty"`
	Repeating   bool      `json:"repeating"`
	LastFailure string    `json:"last_failure,omitempty"`
}
type Args struct {
	Action, Schedule, Prompt, Name, Skill, JobID, Deliver string
	Skills                                                []string
	Repeat                                                int
}
type Reply struct {
	Job     Job    `json:"job,omitempty"`
	Jobs    []Job  `json:"jobs,omitempty"`
	Message string `json:"message,omitempty"`
}
type Result struct {
	Answer  string
	Failed  bool
	Failure string
}
type Runner func(context.Context, Job) Result
type Manager struct {
	mu     sync.Mutex
	runMu  sync.Mutex
	root   string
	now    func() time.Time
	run    Runner
	jobs   []Job
	notice func(string)
	finish func(Job, Result, bool)
}

func New(profileRoot string, now func() time.Time, run Runner) *Manager {
	if now == nil {
		now = time.Now
	}
	m := &Manager{root: filepath.Join(profileRoot, "cron"), now: now, run: run}
	_ = m.load()
	return m
}
func (m *Manager) Root() string         { return m.root }
func (m *Manager) SetRunner(run Runner) { m.mu.Lock(); m.run = run; m.mu.Unlock() }
func (m *Manager) SetProfileRoot(root string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.root = filepath.Join(root, "cron")
	m.jobs = nil
	return m.load()
}
func (m *Manager) SetHooks(notice func(string), finish func(Job, Result, bool)) {
	m.mu.Lock()
	m.notice, m.finish = notice, finish
	m.mu.Unlock()
}
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Job(nil), m.jobs...)
}
func (m *Manager) StartNow(ref string) (Job, error) {
	m.mu.Lock()
	i, err := m.findLocked(ref)
	if err != nil {
		m.mu.Unlock()
		return Job{}, err
	}
	j := m.jobs[i]
	m.mu.Unlock()
	go func() { _ = m.runOne(context.Background(), j, true) }()
	return j, nil
}
func (m *Manager) load() error {
	data, err := os.ReadFile(filepath.Join(m.root, "jobs.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &m.jobs)
}
func (m *Manager) saveLocked() error {
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.jobs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(m.root, ".jobs.json.tmp")
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(m.root, "jobs.json"))
}
func id() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }
func (m *Manager) findLocked(ref string) (int, error) {
	for i := range m.jobs {
		if m.jobs[i].ID == ref {
			return i, nil
		}
	}
	hits := []int{}
	for i := range m.jobs {
		if strings.EqualFold(m.jobs[i].Name, ref) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	if len(hits) > 1 {
		names := []string{}
		for _, i := range hits {
			names = append(names, m.jobs[i].ID+" ("+m.jobs[i].Name+")")
		}
		return -1, fmt.Errorf("job name is ambiguous; candidates: %s", strings.Join(names, ", "))
	}
	return -1, fmt.Errorf("job %q not found", ref)
}

func (m *Manager) Apply(ctx context.Context, a Args) (Reply, error) {
	m.mu.Lock()
	if a.Skill != "" {
		a.Skills = append(a.Skills, a.Skill)
	}
	switch a.Action {
	case "create":
		if len(m.jobs) >= maxJobs {
			m.mu.Unlock()
			return Reply{}, fmt.Errorf("job limit reached (%d)", maxJobs)
		}
		spec, err := Parse(a.Schedule, m.now())
		if err != nil {
			m.mu.Unlock()
			return Reply{}, err
		}
		if strings.TrimSpace(a.Prompt) == "" {
			m.mu.Unlock()
			return Reply{}, errors.New("prompt is required")
		}
		j := Job{ID: id(), Name: strings.TrimSpace(a.Name), Schedule: a.Schedule, Prompt: a.Prompt, Skills: append([]string(nil), a.Skills...), Deliver: a.Deliver, Repeat: a.Repeat, Next: spec.Next, Repeating: spec.Repeating}
		if j.Name == "" {
			j.Name = j.ID
		}
		m.jobs = append(m.jobs, j)
		err = m.saveLocked()
		m.mu.Unlock()
		return Reply{Job: j, Message: "created " + j.Name}, err
	case "list":
		jobs := append([]Job(nil), m.jobs...)
		m.mu.Unlock()
		return Reply{Jobs: jobs}, nil
	case "update":
		i, err := m.findLocked(a.JobID)
		if err != nil {
			m.mu.Unlock()
			return Reply{}, err
		}
		j := m.jobs[i]
		if a.Schedule != "" {
			spec, e := Parse(a.Schedule, m.now())
			if e != nil {
				m.mu.Unlock()
				return Reply{}, e
			}
			j.Schedule, j.Next, j.Repeating = a.Schedule, spec.Next, spec.Repeating
		}
		if a.Prompt != "" {
			j.Prompt = a.Prompt
		}
		if a.Name != "" {
			j.Name = a.Name
		}
		if a.Skills != nil {
			j.Skills = append([]string(nil), a.Skills...)
		}
		if a.Deliver != "" {
			j.Deliver = a.Deliver
		}
		if a.Repeat > 0 {
			j.Repeat = a.Repeat
		}
		m.jobs[i] = j
		err = m.saveLocked()
		m.mu.Unlock()
		return Reply{Job: j, Message: "updated " + j.Name}, err
	case "pause", "resume":
		i, err := m.findLocked(a.JobID)
		if err != nil {
			m.mu.Unlock()
			return Reply{}, err
		}
		m.jobs[i].Paused = a.Action == "pause"
		j := m.jobs[i]
		err = m.saveLocked()
		m.mu.Unlock()
		return Reply{Job: j, Message: a.Action + "d " + j.Name}, err
	case "remove":
		i, err := m.findLocked(a.JobID)
		if err != nil {
			m.mu.Unlock()
			return Reply{}, err
		}
		j := m.jobs[i]
		m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
		err = m.saveLocked()
		m.mu.Unlock()
		return Reply{Job: j, Message: "removed " + j.Name}, err
	case "run":
		i, err := m.findLocked(a.JobID)
		if err != nil {
			m.mu.Unlock()
			return Reply{}, err
		}
		j := m.jobs[i]
		m.mu.Unlock()
		err = m.runOne(ctx, j, true)
		return Reply{Job: j, Message: "started " + j.Name}, err
	default:
		m.mu.Unlock()
		return Reply{}, errors.New("action must be create, list, update, pause, resume, run, or remove")
	}
}

func (m *Manager) Tick(ctx context.Context) error {
	now := m.now()
	m.mu.Lock()
	due := []Job{}
	for _, j := range m.jobs {
		if !j.Paused && !j.Next.After(now) {
			due = append(due, j)
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].Next.Before(due[j].Next) })
	m.mu.Unlock()
	for _, j := range due {
		if err := m.runOne(ctx, j, false); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) runOne(ctx context.Context, j Job, manual bool) error {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.run == nil {
		return errors.New("scheduled runner is unavailable")
	}
	result := m.run(ctx, j)
	stamp := m.now().UTC().Format("20060102T150405.000000000Z")
	dir := filepath.Join(m.root, "output", j.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	body := result.Answer
	if result.Failed && result.Failure != "" {
		body += "\n\nFailure: " + result.Failure
	}
	if err := os.WriteFile(filepath.Join(dir, stamp+".md"), []byte(body+"\n"), 0600); err != nil {
		return err
	}
	if entries, _ := os.ReadDir(dir); len(entries) > maxOutputsPerJob {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries[:len(entries)-maxOutputsPerJob] {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	silent := strings.Contains(result.Answer, "[SILENT]") && !result.Failed
	keep := !silent
	m.mu.Lock()
	i, e := m.findLocked(j.ID)
	if e == nil {
		current := m.jobs[i]
		current.Runs++
		notify := !silent
		if result.Failed {
			if current.LastFailure == result.Failure {
				notify = false
			}
			current.LastFailure = result.Failure
		} else {
			current.LastFailure = ""
		}
		remove := !current.Repeating || (current.Repeat > 0 && current.Runs >= current.Repeat)
		if remove {
			m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
		} else {
			spec, _ := Parse(current.Schedule, m.now())
			current.Next = spec.Next
			m.jobs[i] = current
		}
		_ = m.saveLocked()
		notice, finish := m.notice, m.finish
		m.mu.Unlock()
		if notify && notice != nil {
			notice(j.Name)
		}
		if finish != nil {
			finish(j, result, keep)
		}
	} else {
		m.mu.Unlock()
	}
	_ = manual
	return nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	_ = m.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.Tick(ctx)
		}
	}
}
