package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Item 2wg: the same loop key giving the same result is told once at
// loopSteerAt and reaches the cycle decision at loopCycleAt. Not settings.
const (
	loopSteerAt = 3
	loopCycleAt = 6
)

type guardPair struct {
	argsHash, resultHash string
	callID               string
}

type loopStreak struct {
	resultHash, firstCallID string
	count                   int
	steered                 bool
}

type runGuards struct {
	window  int
	recent  []guardPair
	key     func(string, map[string]any) string
	streaks map[string]*loopStreak
}

func newRunGuards(window int) *runGuards {
	return &runGuards{window: window}
}

// withLoopKey counts repeats per tool's loop key as well as consecutively.
func (g *runGuards) withLoopKey(key func(string, map[string]any) string) *runGuards {
	g.key, g.streaks = key, map[string]*loopStreak{}
	return g
}

func (g *runGuards) Observe(callID, name string, args map[string]any, result string, ok bool) (reason, detail, priorCallID string) {
	argsJSON, _ := json.Marshal(args)
	argsHash := digest(name + string(argsJSON))
	resultHash := digest(result)
	if g.window <= 0 {
		return "", "", ""
	}
	consecutive := 0
	for index := len(g.recent) - 1; index >= 0; index-- {
		prior := g.recent[index]
		if prior.argsHash != argsHash || prior.resultHash != resultHash {
			break
		}
		consecutive++
		priorCallID = prior.callID
	}
	g.recent = append(g.recent, guardPair{argsHash: argsHash, resultHash: resultHash, callID: callID})
	if len(g.recent) > g.window {
		g.recent = append([]guardPair(nil), g.recent[len(g.recent)-g.window:]...)
	}
	if consecutive+1 >= g.window {
		return "cycle", name + " repeated with identical arguments and identical result", priorCallID
	}
	if g.key == nil {
		return "", "", ""
	}
	value := g.key(name, args)
	if value == "" {
		value = string(argsJSON)
	}
	streak := g.streaks[name+"\x00"+value]
	if streak == nil {
		streak = &loopStreak{}
		g.streaks[name+"\x00"+value] = streak
	}
	if streak.resultHash != resultHash {
		streak.resultHash, streak.firstCallID, streak.count = resultHash, callID, 0
	}
	streak.count++
	switch {
	case streak.count >= loopCycleAt:
		return "cycle", fmt.Sprintf("%s on %s gave the same result %d times", name, shortLoopKey(value), streak.count), streak.firstCallID
	case streak.count == loopSteerAt && !streak.steered:
		streak.steered = true
		return "steer", steerLine(name, value), ""
	}
	return "", "", ""
}

func (g *runGuards) ResetCycle() {
	g.recent = nil
	for _, streak := range g.streaks {
		streak.count = 0
	}
}

func steerLine(tool, value string) string {
	return fmt.Sprintf("%s on %s has given the same result %d times; say what you need from the user, or finish.", tool, shortLoopKey(value), loopSteerAt)
}

// shortLoopKey keeps a path's last four parts, any other value's first 60 characters.
func shortLoopKey(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) > 4 {
		return "…/" + strings.Join(parts[len(parts)-4:], "/")
	}
	if runes := []rune(value); len(runes) > 60 {
		return string(runes[:57]) + "…"
	}
	return value
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
