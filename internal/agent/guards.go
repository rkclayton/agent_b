package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type guardPair struct {
	argsHash, resultHash string
	callID               string
}
type runGuards struct {
	window int
	recent []guardPair
}

func newRunGuards(window int) *runGuards {
	return &runGuards{window: window}
}
func (g *runGuards) Observe(callID, name string, args map[string]any, result string, ok bool) (reason, detail, priorCallID string) {
	argsJSON, _ := json.Marshal(args)
	argsHash := digest(name + string(argsJSON))
	resultHash := digest(result)
	if g.window > 0 {
		consecutive := 0
		priorCallID := ""
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
	}
	return "", "", ""
}
func (g *runGuards) ResetCycle() { g.recent = nil }
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
