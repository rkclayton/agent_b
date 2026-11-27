package agent

import (
	"fmt"

	"harness/internal/events"
	"harness/internal/session"
)

// unattended means NOTHING ASKS.
//
// Two settings that are deliberately independent. WHO RUNS IT is the service
// account or the operator ("Run as you"), and that is about identity. DOES IT
// ASK is attended or unattended, and that is about interruption. Unattended
// changes nothing about who runs anything and nothing about what the boundary
// permits: a thing that was refused is still refused. What changes is that
// nobody is asked about it at three in the morning - the attempt is recorded as
// a boundary failure on the item, the run moves on, and the report lists every
// one.
//
// A chat the operator is typing in stays attended whatever the switch says. He
// is there; asking him is not an interruption, it is the point. Unattended is
// for scheduled runs.

const (
	boundaryPolicy = "policy approval"
	boundaryEscape = "identity escalation"
	boundaryCycle  = "cycle decision"
)

// unattended is chosen on the job and inherited by a delegated run.
func (g *Gate) unattended(s *session.Session) bool {
	return s.Unattended
}

// refuseUnattended records the boundary hit and returns the decision the gate
// would have received from an operator who declined.
func (g *Gate) refuseUnattended(s *session.Session, runID, callID, kind, name string, args map[string]any) string {
	reason := fmt.Sprintf("[!] boundary: %s (%s)", kind, name)
	g.bus.Publish(events.New(events.ApprovalDecided, s.ID, runID, map[string]any{
		"call_id": callID, "name": name, "decision": "deny", "unattended": true, "boundary": reason, "args": args,
	}))
	s.RecordBoundaryHit(reason)
	return "deny"
}
