package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"harness/internal/events"
	"harness/internal/session"
)

// The plain proposal form (item 2fk), for a planner model that drifts from the
// fenced block: a line that is exactly "Proposals:" followed by numbered lines
//
//  1. add: <the new plan line>
//  2. reword: <exact current line | item N> => <new text>
//  3. reorder: <exact current line | item N> => after: <exact current line | item N>
//  4. drop: <exact current line | item N>
//
// Each line becomes the same exact-span proposal the fenced form carries,
// resolved against the plan's current plan.md, so the tray validates and Accept
// applies it exactly as it would a fenced one. A line that does not resolve
// stays ordinary prose.
var (
	plainProposalHeading = regexp.MustCompile(`(?m)^[ \t]*(?:#+[ \t]*)?\**Proposals:?\**[ \t]*$`)
	plainProposalLine    = regexp.MustCompile(`^[ \t]*\d+[.)][ \t]+\**(add|reword|reorder|drop)\**[ \t]*:[ \t]*(.+?)[ \t]*$`)
	planLineItemID       = regexp.MustCompile(`\[\[([0-9]+[a-z]*)\]\]`)
	planItemReference    = regexp.MustCompile(`(?i)^item[ \t]+\[?\[?([0-9]+[a-z]*)\]?\]?$`)
	plainArrow           = regexp.MustCompile(`[ \t]*(?:=>|→)[ \t]*`)
)

// parsePlainPlanProposals turns a plain "Proposals:" list into proposals. It
// returns the text with the resolved lines removed, and the proposals.
func parsePlainPlanProposals(content, planText string) (string, []events.PlanProposal) {
	heading := plainProposalHeading.FindStringIndex(content)
	if heading == nil || strings.TrimSpace(planText) == "" {
		return content, nil
	}
	planLines := strings.Split(strings.ReplaceAll(planText, "\r\n", "\n"), "\n")
	lines := strings.Split(content[heading[1]:], "\n")
	kept := []string{}
	proposals := []events.PlanProposal{}
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := plainProposalLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if match == nil {
			// The list ends at the first line that is not a proposal.
			kept = append(kept, lines[index:]...)
			break
		}
		proposal, ok := plainProposal(strings.ToLower(match[1]), match[2], planLines, len(proposals)+1)
		if !ok {
			kept = append(kept, line)
			continue
		}
		proposals = append(proposals, proposal)
	}
	if len(proposals) == 0 {
		return content, nil
	}
	visible := strings.TrimSpace(content[:heading[0]] + strings.Join(kept, "\n"))
	return visible, proposals
}

func plainProposal(kind, body string, planLines []string, number int) (events.PlanProposal, bool) {
	proposal := events.PlanProposal{ID: fmt.Sprintf("plain-%d", number), Kind: kind, Path: "plan.md"}
	switch kind {
	case "add":
		anchor := addAnchor(planLines)
		text := strings.TrimSpace(body)
		if anchor == "" || text == "" {
			return proposal, false
		}
		if !strings.HasPrefix(text, "- [") {
			text = "- [ ] " + text
		}
		proposal.OldText, proposal.NewText, proposal.ItemID = anchor, anchor+"\n"+text, fmt.Sprintf("new-%d", number)
		return proposal, true
	case "drop":
		line, ok := resolvePlanLine(body, planLines)
		if !ok {
			return proposal, false
		}
		proposal.OldText, proposal.NewText, proposal.ItemID = line+"\n", "", planItemID(line)
		return proposal, true
	case "reword":
		parts := plainArrow.Split(body, 2)
		if len(parts) != 2 {
			return proposal, false
		}
		line, ok := resolvePlanLine(parts[0], planLines)
		replacement := strings.TrimSpace(parts[1])
		if !ok || replacement == "" {
			return proposal, false
		}
		// Keep the line's marker and id when the model gave only the words.
		if prefix := planLinePrefix(line); prefix != "" && !strings.HasPrefix(replacement, "- [") {
			replacement = prefix + replacement
		}
		if replacement == line {
			return proposal, false
		}
		proposal.OldText, proposal.NewText, proposal.ItemID = line, replacement, planItemID(line)
		return proposal, true
	case "reorder":
		parts := plainArrow.Split(body, 2)
		if len(parts) != 2 {
			return proposal, false
		}
		target := strings.TrimSpace(parts[1])
		lower := strings.ToLower(target)
		if !strings.HasPrefix(lower, "after:") && !strings.HasPrefix(lower, "after ") {
			return proposal, false
		}
		moved, okMoved := resolvePlanLine(parts[0], planLines)
		anchor, okAnchor := resolvePlanLine(strings.TrimLeft(strings.TrimSpace(target[len("after"):]), ": \t"), planLines)
		if !okMoved || !okAnchor || moved == anchor {
			return proposal, false
		}
		from, to := indexOfLine(planLines, moved), indexOfLine(planLines, anchor)
		start, end := min(from, to), max(from, to)
		span := append([]string(nil), planLines[start:end+1]...)
		reordered := []string{}
		for _, line := range span {
			if line == moved {
				continue
			}
			reordered = append(reordered, line)
			if line == anchor {
				reordered = append(reordered, moved)
			}
		}
		if strings.Join(reordered, "\n") == strings.Join(span, "\n") {
			return proposal, false
		}
		proposal.OldText, proposal.NewText, proposal.ItemID = strings.Join(span, "\n"), strings.Join(reordered, "\n"), planItemID(moved)
		return proposal, true
	}
	return proposal, false
}

// resolvePlanLine finds the plan line a reference names: "item N" by its
// [[N]] id, otherwise the line whose text is exactly the reference (with or
// without its marker and id).
func resolvePlanLine(reference string, planLines []string) (string, bool) {
	reference = strings.Trim(strings.TrimSpace(reference), "\"'`")
	if match := planItemReference.FindStringSubmatch(reference); match != nil {
		for _, line := range planLines {
			if id := planLineItemID.FindStringSubmatch(line); id != nil && id[1] == strings.ToLower(match[1]) {
				return line, true
			}
		}
		return "", false
	}
	for _, line := range planLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == reference || strings.TrimSpace(strings.TrimPrefix(trimmed, planLinePrefix(trimmed))) == reference {
			return line, true
		}
	}
	return "", false
}

var planLineMarker = regexp.MustCompile(`^\s*[-*]\s+\[[ x~!-]\]\s+(?:\[\[[0-9]+[a-z]*\]\]\s+)?`)

// planLinePrefix is a line's leading marker and id ("- [ ] [[3]] "), or "".
func planLinePrefix(line string) string {
	return planLineMarker.FindString(line)
}

func planItemID(line string) string {
	if match := planLineItemID.FindStringSubmatch(line); match != nil {
		return match[1]
	}
	return "plan"
}

// addAnchor is where a new item goes: after the last item line; in a plan with
// none yet (2bq's template), after its "Current work order" heading; otherwise
// after the last line.
func addAnchor(lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		if planLineMarker.MatchString(lines[index]) {
			return lines[index]
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimLeft(line, "#")), "Current work order") && strings.HasPrefix(line, "#") {
			return line
		}
	}
	return lastNonEmpty(lines)
}

func lastNonEmpty(lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) != "" {
			return lines[index]
		}
	}
	return ""
}

func indexOfLine(lines []string, line string) int {
	for index, candidate := range lines {
		if candidate == line {
			return index
		}
	}
	return -1
}

// planProposalsFor parses a model turn's proposals: the fenced block first, and
// for a design session bound to a plan, the plain "Proposals:" form when the
// turn carried no fenced block (item 2fk).
func planProposalsFor(s *session.Session, content string) (string, []events.PlanProposal) {
	visible, proposals := parsePlanProposals(content)
	if len(proposals) > 0 || s.Role != "d" || s.PlanDir == "" {
		return visible, proposals
	}
	plan, err := os.ReadFile(filepath.Join(s.PlanDir, "plan.md"))
	if err != nil {
		return visible, proposals
	}
	return parsePlainPlanProposals(visible, string(plan))
}
