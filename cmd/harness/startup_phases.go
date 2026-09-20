package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Item 2gm (v1.1.3/W2): the launch is measured rather than guessed. The
// operator reported "it eventually loaded, just took a good 2 minutes to
// launch", and the v0.66.0 report had already measured 45-74 s to listen and
// carded the cause as unknown — the launcher's ready wait was raised to 120 s
// to cover it instead. A phase line in the log says where the time goes, on
// every start, so the next person does not have to guess either.
//
// The timer is deliberately dumb: no sampling, no averaging, one elapsed
// reading per named phase and one summary line at listen.

// startupTimer is the one in flight. It is package level because the listen
// line lives in serve(), not in main().
var startupTimer *startupPhases

type startupPhases struct {
	mu      sync.Mutex
	started time.Time
	last    time.Time
	order   []string
	spent   map[string]time.Duration
	done    bool
}

func newStartupPhases() *startupPhases {
	now := time.Now()
	return &startupPhases{started: now, last: now, spent: map[string]time.Duration{}}
}

// mark closes the phase that has been running since the last mark and names it.
func (p *startupPhases) mark(name string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	now := time.Now()
	elapsed := now.Sub(p.last)
	p.last = now
	if _, seen := p.spent[name]; !seen {
		p.order = append(p.order, name)
	}
	p.spent[name] += elapsed
}

// listening closes the last phase and writes the summary. Everything after it
// is by definition not delaying the port, which is the whole point of the
// measurement: a phase that moves after this line stops costing the operator
// anything.
func (p *startupPhases) listening() string {
	if p == nil {
		return ""
	}
	p.mark("listen")
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = true
	total := time.Since(p.started)
	parts := make([]string, 0, len(p.order))
	for _, name := range p.order {
		parts = append(parts, fmt.Sprintf("%s %d ms", name, p.spent[name].Milliseconds()))
	}
	return fmt.Sprintf("startup phases: %s; total to listen %d ms", strings.Join(parts, " · "), total.Milliseconds())
}

// report writes the summary to the log.
func (p *startupPhases) report() {
	if line := p.listening(); line != "" {
		log.Print(line)
	}
}
