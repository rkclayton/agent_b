package web

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"harness/internal/reflection"
	"harness/internal/session"
)

// Reflection's plan proposals, offered through the card that already exists
// (v1.1.1/W3). Reflection finds repositories recent activity touched that have
// agent files and no plan; it registers nothing. The operator meets one
// ordinary Allow-this card per repository on their next visit, marked as
// proposed by reflection and carrying the activity that led to it. Approving
// registers the plan through the same path the model's own proposal uses;
// declining suppresses that repository until its agent files change.

// proposalOfferName is the card's name, which is also its marker line: the
// model's own proposal reads "plan registration proposed by agent_b".
const proposalOfferName = "plan registration proposed by reflection"

// proposalOfferTimeout bounds one offer. A card nobody answers is withdrawn
// and the proposal stays pending for the next visit — an unanswered card is
// not a decision.
const proposalOfferTimeout = 10 * time.Minute

type proposalOffers struct {
	mu       sync.Mutex
	inFlight map[string]bool
}

func newProposalOffers() *proposalOffers { return &proposalOffers{inFlight: map[string]bool{}} }

func (o *proposalOffers) begin(root string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.inFlight[root] {
		return false
	}
	o.inFlight[root] = true
	return true
}

func (o *proposalOffers) done(root string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.inFlight, root)
}

// OfferReflectionProposals raises at most one pending proposal, against an
// idle chat the operator is looking at. It returns immediately: the card is
// answered in its own goroutine, like every other card.
func (s *Server) OfferReflectionProposals() {
	state := s.reflectionNow()
	if state == nil || s.runner == nil || s.registry == nil {
		return
	}
	pending, err := state.store.PendingProposals(1)
	if err != nil || len(pending) == 0 {
		return
	}
	proposal := pending[0]
	item := s.sessionForProposal()
	if item == nil {
		return
	}
	if !s.proposals.begin(proposal.Root) {
		return
	}
	state.inFlight.Add(1)
	go func() {
		defer state.inFlight.Done()
		defer s.proposals.done(proposal.Root)
		s.offerProposal(state, item, proposal)
	}()
}

// sessionForProposal is an idle b-role chat: the operator is here and the
// chat is not in the middle of a run. One proposal is offered at a time, so
// the operator never meets two of these at once.
func (s *Server) sessionForProposal() *session.Session {
	for _, item := range s.registry.List() {
		snapshot := item.Snapshot()
		if snapshot.Role != "" && snapshot.Role != "b" {
			continue
		}
		if snapshot.Run.Status != "idle" && snapshot.Run.Status != "" {
			continue
		}
		return item
	}
	return nil
}

func (s *Server) offerProposal(state *reflectionState, item *session.Session, proposal reflection.Proposal) {
	// The repository may have been registered, or its agent files removed,
	// since the pass that proposed it.
	if planRegistered(item, proposal.Root) {
		_ = state.store.SetProposalState(proposal.Root, reflection.ProposalApproved, time.Now())
		return
	}
	if reflection.AgentFileFingerprint(proposal.Root) == "" {
		_ = state.store.SetProposalState(proposal.Root, reflection.ProposalDeclined, time.Now())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), proposalOfferTimeout)
	defer cancel()
	callID := "reflection-proposal-" + strings.ToLower(filepath.Base(proposal.Root)) + "-" + time.Now().UTC().Format("150405")
	args := map[string]any{
		"path":        proposal.Root,
		"proposed_by": "reflection",
		"activity":    proposal.Activity,
	}
	approved, err := s.runner.Gate().WaitPolicyRequired(ctx, item, "", callID, proposalOfferName, args)
	if err != nil {
		// Nobody answered, or the wait was cancelled: the proposal stays
		// pending and is offered again on the next visit.
		log.Printf("reflection: the plan proposal for %s was not answered: %v", proposal.Root, err)
		return
	}
	if !approved {
		if err := state.store.SetProposalState(proposal.Root, reflection.ProposalDeclined, time.Now()); err != nil {
			log.Printf("reflection: recording the declined proposal for %s: %v", proposal.Root, err)
		}
		return
	}
	if item.RegisterPlan == nil {
		log.Printf("reflection: plan registration is unavailable; %s stays pending", proposal.Root)
		return
	}
	plan, created, registerErr := item.RegisterPlan(proposal.Root)
	if registerErr != nil {
		log.Printf("reflection: registering %s: %v", proposal.Root, registerErr)
		return
	}
	if err := state.store.SetProposalState(proposal.Root, reflection.ProposalApproved, time.Now()); err != nil {
		log.Printf("reflection: recording the approved proposal for %s: %v", proposal.Root, err)
	}
	log.Printf("reflection: %s registered as plan %q (created=%t) after the operator approved the proposal", proposal.Root, plan.Name, created)
}

// planRegistered says whether a folder is already one of the registered plan
// repositories a chat may work in.
func planRegistered(item *session.Session, root string) bool {
	if item == nil || item.PlanRepos == nil {
		return false
	}
	target, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	for _, repo := range item.PlanRepos() {
		if existing, err := filepath.Abs(repo); err == nil && strings.EqualFold(existing, target) {
			return true
		}
	}
	return false
}
