package agent

import (
	"encoding/json"
	"strings"

	"harness/internal/events"
)

const planProposalFence = "```agentb-plan-proposals"

type planProposalEnvelope struct {
	Version   int                   `json:"version"`
	Proposals []events.PlanProposal `json:"proposals"`
}

// parsePlanProposals removes only well-formed version-one proposal blocks.
// Malformed model output remains ordinary visible prose and cannot enter the tray.
func parsePlanProposals(content string) (string, []events.PlanProposal) {
	visible := content
	proposals := []events.PlanProposal{}
	for {
		start := strings.Index(visible, planProposalFence)
		if start < 0 {
			break
		}
		bodyStart := start + len(planProposalFence)
		if bodyStart < len(visible) && visible[bodyStart] == '\r' {
			bodyStart++
		}
		if bodyStart >= len(visible) || visible[bodyStart] != '\n' {
			break
		}
		bodyStart++
		endOffset := strings.Index(visible[bodyStart:], "\n```")
		if endOffset < 0 {
			break
		}
		end := bodyStart + endOffset
		var envelope planProposalEnvelope
		if json.Unmarshal([]byte(visible[bodyStart:end]), &envelope) != nil || envelope.Version != 1 || len(envelope.Proposals) == 0 {
			break
		}
		// Item 2fk: each well-formed proposal lands on its own; one malformed
		// sibling no longer voids the block. A block with any proposal that
		// cannot land stays visible, so nothing the model wrote is hidden.
		landed := []events.PlanProposal{}
		for _, proposal := range envelope.Proposals {
			if proposal.ItemID == "" {
				proposal.ItemID = derivedItemID(proposal)
			}
			if proposal.ID == "" || proposal.Kind == "" || proposal.Path == "" || proposal.OldText == "" || proposal.ItemID == "" {
				continue
			}
			landed = append(landed, proposal)
		}
		if len(landed) == 0 {
			break
		}
		proposals = append(proposals, landed...)
		if len(landed) < len(envelope.Proposals) {
			// Search past this block for the next one.
			rest, more := parsePlanProposals(visible[end+4:])
			return strings.TrimSpace(visible[:end+4] + "\n" + rest), append(proposals, more...)
		}
		visible = visible[:start] + visible[end+4:]
	}
	return strings.TrimSpace(visible), proposals
}

// derivedItemID names the item a proposal serves when the model left item_id
// empty: the item file it edits, else the first [[id]] its text names, else
// the file it touches.
func derivedItemID(proposal events.PlanProposal) string {
	path := strings.ReplaceAll(proposal.Path, "\\", "/")
	if strings.HasPrefix(path, "plan/items/") && strings.HasSuffix(path, ".md") {
		return strings.TrimSuffix(strings.TrimPrefix(path, "plan/items/"), ".md")
	}
	for _, text := range []string{proposal.OldText, proposal.NewText} {
		if match := planLineItemID.FindStringSubmatch(text); match != nil {
			return match[1]
		}
	}
	switch path {
	case "AGENT_B.md":
		return "agent-b"
	case "plan.md":
		return "plan"
	}
	return ""
}

// bindPlanProposalSources makes settlement an observed-transcript decision, not
// a model-controlled list of arbitrary message ids.
func bindPlanProposalSources(messages []events.Message, proposals []events.PlanProposal, assistantID string) []events.PlanProposal {
	userID := ""
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" && !messages[index].Elided {
			userID = messages[index].ID
			break
		}
	}
	for index := range proposals {
		proposals[index].SourceMessageIDs = proposals[index].SourceMessageIDs[:0]
		if userID != "" {
			proposals[index].SourceMessageIDs = append(proposals[index].SourceMessageIDs, userID)
		}
		proposals[index].SourceMessageIDs = append(proposals[index].SourceMessageIDs, assistantID)
	}
	return proposals
}
