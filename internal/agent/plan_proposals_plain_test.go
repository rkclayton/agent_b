package agent

import (
	"strings"
	"testing"
)

const plainPlan = "# Walk plan\n\n## Items\n\n- [ ] [[1]] Write the README introduction\n- [ ] [[2]] Add a contributing guide\n- [ ] [[3]] Set up continuous integration\n- [ ] [[4]] Publish the first release\n"

// Item 2fk: the plain form lands in the tray as the same exact-span proposals
// the fenced block carries, resolved against the plan as it is.
func TestPlainProposalsResolveAgainstThePlan(t *testing.T) {
	content := "Here are four edits.\n\nProposals:\n1. add: Write a changelog\n2. reword: item 2 => Add a contributing guide with a code of conduct\n3. reorder: item 4 => after: item 2\n4. drop: Set up continuous integration\n\nTell me which to keep."
	visible, proposals := parsePlainPlanProposals(content, plainPlan)
	if len(proposals) != 4 {
		t.Fatalf("proposals=%+v", proposals)
	}
	if strings.Contains(visible, "reword:") || !strings.Contains(visible, "Here are four edits.") || !strings.Contains(visible, "Tell me which to keep.") {
		t.Fatalf("visible=%q", visible)
	}
	add, reword, reorder, drop := proposals[0], proposals[1], proposals[2], proposals[3]
	if add.Kind != "add" || add.OldText != "- [ ] [[4]] Publish the first release" || add.NewText != add.OldText+"\n- [ ] Write a changelog" {
		t.Fatalf("add=%+v", add)
	}
	if reword.OldText != "- [ ] [[2]] Add a contributing guide" || reword.NewText != "- [ ] [[2]] Add a contributing guide with a code of conduct" || reword.ItemID != "2" {
		t.Fatalf("reword=%+v", reword)
	}
	if reorder.OldText != "- [ ] [[2]] Add a contributing guide\n- [ ] [[3]] Set up continuous integration\n- [ ] [[4]] Publish the first release" ||
		reorder.NewText != "- [ ] [[2]] Add a contributing guide\n- [ ] [[4]] Publish the first release\n- [ ] [[3]] Set up continuous integration" || reorder.ItemID != "4" {
		t.Fatalf("reorder=%+v", reorder)
	}
	if !sameLines(reorder.OldText, reorder.NewText) {
		t.Fatal("a reorder must keep the same lines")
	}
	if drop.OldText != "- [ ] [[3]] Set up continuous integration\n" || drop.NewText != "" || drop.ItemID != "3" {
		t.Fatalf("drop=%+v", drop)
	}
	for _, proposal := range proposals {
		if !strings.Contains(plainPlan, proposal.OldText) {
			t.Fatalf("%s old_text is not an exact span of the plan: %q", proposal.Kind, proposal.OldText)
		}
	}
}

func TestPlainProposalsThatDoNotResolveStayProse(t *testing.T) {
	for _, content := range []string{
		"Some ideas:\n1. add a changelog\n2. drop the release",
		"Proposals:\n1. reword: item 9 => something\n2. drop: a line that is not in the plan",
		"Proposals:\n1. add: a line",
	} {
		plan := plainPlan
		if strings.HasSuffix(content, "a line") {
			plan = ""
		}
		visible, proposals := parsePlainPlanProposals(content, plan)
		if len(proposals) != 0 || visible != content {
			t.Fatalf("content %q must stay prose: %q %+v", content, visible, proposals)
		}
	}
}

func sameLines(left, right string) bool {
	a, b := strings.Split(left, "\n"), strings.Split(right, "\n")
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, line := range a {
		seen[line]++
	}
	for _, line := range b {
		seen[line]--
	}
	for _, count := range seen {
		if count != 0 {
			return false
		}
	}
	return true
}

func TestAPlainAddGoesAfterTheLastItemOrTheCurrentOrderHeading(t *testing.T) {
	_, proposals := parsePlainPlanProposals("Proposals:\n1. add: Write a changelog", plainPlan+"\n## Index\n\n(generated from plan/items)\n")
	if len(proposals) != 1 || proposals[0].OldText != "- [ ] [[4]] Publish the first release" {
		t.Fatalf("add must follow the last item, not the index: %+v", proposals)
	}
	template := "# P\n\n## Milestones and current order\n\n### Current work order\n\n(items are written here)\n\n## Index\n\n(generated)\n"
	_, proposals = parsePlainPlanProposals("Proposals:\n1. add: First item", template)
	if len(proposals) != 1 || proposals[0].OldText != "### Current work order" || proposals[0].NewText != "### Current work order\n- [ ] First item" {
		t.Fatalf("add to an empty template plan: %+v", proposals)
	}
}

// The walk's after-measurement: a block whose item_id was null, and a block
// with one item file the tray cannot create beside a plan.md add. Each
// well-formed proposal lands; the block stays visible when one could not.
func TestAFencedProposalLandsOnItsOwnAndAMissingItemIDIsDerived(t *testing.T) {
	block := "```agentb-plan-proposals\n" + `{"version":1,"proposals":[{"id":"a","kind":"agent_b_addition","path":"AGENT_B.md","old_text":"- Run the tests.","new_text":"- Run the tests.\n- Keep commits small.","item_id":null,"source_message_ids":[]}]}` + "\n```"
	visible, proposals := parsePlanProposals("Here.\n\n" + block)
	if len(proposals) != 1 || proposals[0].ItemID != "agent-b" || visible != "Here." {
		t.Fatalf("null item_id: visible %q proposals %+v", visible, proposals)
	}
	mixed := "```agentb-plan-proposals\n" + `{"version":1,"proposals":[{"id":"p","kind":"add","path":"plan.md","old_text":"- [ ] [[4]] Publish","new_text":"- [ ] [[4]] Publish\n- [ ] [[5]] Changelog","item_id":"","source_message_ids":[]},{"id":"f","kind":"add","path":"plan/items/5.md","old_text":"","new_text":"# 5","item_id":"5","source_message_ids":[]}]}` + "\n```"
	visible, proposals = parsePlanProposals(mixed + "\n\nAfter.")
	if len(proposals) != 1 || proposals[0].ID != "p" || proposals[0].ItemID != "4" {
		t.Fatalf("mixed block: %+v", proposals)
	}
	if !strings.Contains(visible, `"id":"f"`) || !strings.Contains(visible, "After.") {
		t.Fatalf("a block with a proposal that could not land must stay visible: %q", visible)
	}
	if _, none := parsePlanProposals(strings.Replace(mixed, `"old_text":"- [ ] [[4]] Publish"`, `"old_text":""`, 1)); len(none) != 0 {
		t.Fatalf("a block with nothing well formed lands nothing: %+v", none)
	}
}
