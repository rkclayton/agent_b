package agent

import (
	"fmt"

	"harness/internal/events"
	"harness/internal/session"
)

// Item 5f (v1.2.5): unattended means NOTHING ASKS.
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
// for a worker started by Go and for a scheduled run, which have no one sitting
// in front of them.

// boundaryKinds are the four card kinds the switch answers for. The inventory
// is the scope of this item: a path found later that raises a card without
// passing through one of these is a defect against it.
const (
	boundaryPolicy   = "policy approval"
	boundaryEscape   = "identity escalation"
	boundaryCycle    = "cycle decision"
	boundaryRegister = "plan registration"
)

// unattended says whether this session answers its own cards. It is the
// operator's switch AND the absence of an operator: a b or d chat is one he is
// typing in, so it is attended regardless.
func (g *Gate) unattended(s *session.Session) bool {
	if !g.cfg().Approval.Unattended {
		return false
	}
	return s.Role == "c" || s.Role == "e"
}

// refuseUnattended records the boundary hit and returns the decision the gate
// would have got from an operator who said no. The text is what the item names:
// `[!] boundary: <what>`, so the worker's own reason line and the morning
// report read the same way.
func (g *Gate) refuseUnattended(s *session.Session, runID, callID, kind, name string, args map[string]any) string {
	reason := fmt.Sprintf("[!] boundary: %s (%s)", kind, name)
	g.bus.Publish(events.New(events.ApprovalDecided, s.ID, runID, map[string]any{
		"call_id": callID, "name": name, "decision": "deny", "unattended": true, "boundary": reason, "args": args,
	}))
	s.RecordBoundaryHit(reason)
	return "deny"
}
