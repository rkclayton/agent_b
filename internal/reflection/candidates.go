package reflection

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// The tool-candidate report (item 17-i, step 4). The pipeline is deterministic
// from end to end: no model is called anywhere in this file. It reads the
// recorded tool calls and their results, normalises each call to a shape with
// the accidental constants removed, clusters the shapes, contrasts the calls
// that worked against the ones that did not, and ranks by frequency × failure
// rate. What the report says can be checked against the counts behind it.

// Call is one recorded tool call and its outcome.
type Call struct {
	Tool    string
	Text    string
	Shape   string
	OK      bool
	Failure string
	Session string
}

// Cluster is one normalised shape with its counts and its contrast.
type Cluster struct {
	Tool            string   `json:"tool"`
	Shape           string   `json:"shape"`
	Count           int      `json:"count"`
	Failures        int      `json:"failures"`
	FailureRate     float64  `json:"failure_rate"`
	Score           float64  `json:"score"`
	FailureCauses   []string `json:"failure_causes"`
	Contrast        string   `json:"contrast"`
	Representatives []string `json:"representatives"`
}

var (
	quoted      = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	windowsPath = regexp.MustCompile(`(?i)\b[a-z]:\\[^\s"';|]*|\\\\[^\s"';|]+`)
	posixPath   = regexp.MustCompile(`(?:\./|/)?[\w.\-]+(?:/[\w.\-]+)+`)
	fileName    = regexp.MustCompile(`\b[\w.\-]+\.[A-Za-z][\w]{0,4}\b`)
	numbers     = regexp.MustCompile(`\b\d[\d,.]*\b`)
	// The chain and sequence operators collapse to one token, so a command and
	// the retry that only changed its separator land in the same cluster and
	// the contrast can name the difference (item 17-i's own example).
	separators    = regexp.MustCompile(`\s*(?:&&|\|\||;)\s*`)
	hexes         = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	whitespaceRun = regexp.MustCompile(`\s+`)
)

// normalize strips the accidental constants: quoted strings, absolute and
// relative paths, file names, numbers and hex ids. `node --check game.js` and
// `node --check js/game.js` collapse to the same shape.
func normalize(text string) string {
	shape := text
	shape = quoted.ReplaceAllString(shape, `"S"`)
	shape = windowsPath.ReplaceAllString(shape, "PATH")
	shape = posixPath.ReplaceAllString(shape, "PATH")
	shape = hexes.ReplaceAllString(shape, "ID")
	shape = fileName.ReplaceAllString(shape, "PATH")
	shape = numbers.ReplaceAllString(shape, "N")
	shape = separators.ReplaceAllString(shape, " THEN ")
	shape = whitespaceRun.ReplaceAllString(shape, " ")
	return strings.TrimSpace(shape)
}

// callText is what a call is clustered on: the command for shell and
// run_script, the key argument for the file tools.
func callText(tool string, args map[string]any) string {
	for _, key := range []string{"command", "source", "path", "pattern", "query", "url", "note"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	rendered, _ := json.Marshal(args)
	return string(rendered)
}

// failureCause groups a failure by the text of its error, with the incidental
// parts removed, so causes can be counted.
func failureCause(preview string) string {
	// The known markers are looked for in the whole result: the first line is
	// often only "command failed", with the cause below it.
	whole := strings.ToLower(preview)
	first := strings.TrimSpace(preview)
	if index := strings.IndexAny(first, "\r\n"); index >= 0 {
		first = first[:index]
	}
	first = strings.TrimPrefix(first, "error: ")
	switch {
	case strings.Contains(whole, "not a valid statement separator"):
		return "PowerShell 5.1 rejects && or ||"
	case strings.Contains(whole, "outside the folder"):
		return "a path outside the folder"
	case strings.Contains(whole, "cannot find") || strings.Contains(whole, "no such file"):
		return "the path does not exist"
	case strings.Contains(whole, "exit="):
		return "the command exited nonzero"
	}
	shape := normalize(first)
	if len(shape) > 90 {
		shape = shape[:90]
	}
	if shape == "" {
		return "an unnamed failure"
	}
	return shape
}

// CallsFromJSONL reads tool calls and their results from recorded event logs.
// The JSONL is the authority; nothing here writes.
func CallsFromJSONL(paths []string) ([]Call, error) {
	calls := []Call{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read tool calls: %w", err)
		}
		pending := map[string]*Call{}
		order := []string{}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for scanner.Scan() {
			var row struct {
				SessionID string `json:"session_id"`
				Type      string `json:"type"`
				Data      struct {
					CallID  string         `json:"call_id"`
					Name    string         `json:"name"`
					Args    map[string]any `json:"args"`
					OK      *bool          `json:"ok"`
					Preview string         `json:"preview"`
				} `json:"data"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				continue
			}
			switch row.Type {
			case "tool.call":
				if row.Data.CallID == "" || row.Data.Name == "" {
					continue
				}
				text := callText(row.Data.Name, row.Data.Args)
				pending[row.Data.CallID] = &Call{Tool: row.Data.Name, Text: text, Shape: normalize(text), OK: true, Session: row.SessionID}
				order = append(order, row.Data.CallID)
			case "tool.result":
				call, ok := pending[row.Data.CallID]
				if !ok {
					continue
				}
				if row.Data.OK != nil && !*row.Data.OK {
					call.OK = false
					call.Failure = failureCause(row.Data.Preview)
				}
			}
		}
		if err := scanner.Err(); err != nil {
			file.Close()
			return nil, fmt.Errorf("read tool calls from %s: %w", path, err)
		}
		file.Close()
		for _, id := range order {
			calls = append(calls, *pending[id])
		}
	}
	return calls, nil
}

// Clusters groups calls by tool and shape, contrasts the successes against the
// failures in each, and ranks by frequency × failure rate.
func Clusters(calls []Call) []Cluster {
	type group struct {
		cluster    Cluster
		causes     map[string]int
		successes  []string
		failures   []string
		successful map[string]int
		failing    map[string]int
	}
	groups := map[string]*group{}
	for _, call := range calls {
		key := call.Tool + "\x00" + call.Shape
		current, ok := groups[key]
		if !ok {
			current = &group{cluster: Cluster{Tool: call.Tool, Shape: call.Shape}, causes: map[string]int{}, successful: map[string]int{}, failing: map[string]int{}}
			groups[key] = current
		}
		current.cluster.Count++
		if call.OK {
			if len(current.successes) < 3 {
				current.successes = append(current.successes, call.Text)
			}
			for _, token := range tokensOf(call.Text) {
				current.successful[token]++
			}
			continue
		}
		current.cluster.Failures++
		current.causes[call.Failure]++
		if len(current.failures) < 3 {
			current.failures = append(current.failures, call.Text)
		}
		for _, token := range tokensOf(call.Text) {
			current.failing[token]++
		}
	}
	clusters := make([]Cluster, 0, len(groups))
	for _, current := range groups {
		cluster := current.cluster
		cluster.FailureRate = float64(cluster.Failures) / float64(cluster.Count)
		cluster.Score = float64(cluster.Count) * cluster.FailureRate
		for cause, count := range current.causes {
			cluster.FailureCauses = append(cluster.FailureCauses, fmt.Sprintf("%s (%d)", cause, count))
		}
		sort.Strings(cluster.FailureCauses)
		cluster.Contrast = contrast(current.successful, current.failing, cluster.Failures, cluster.Count-cluster.Failures)
		cluster.Representatives = append(append([]string{}, current.failures...), current.successes...)
		clusters = append(clusters, cluster)
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].Score != clusters[j].Score {
			return clusters[i].Score > clusters[j].Score
		}
		if clusters[i].Count != clusters[j].Count {
			return clusters[i].Count > clusters[j].Count
		}
		return clusters[i].Shape < clusters[j].Shape
	})
	return clusters
}

// tokensOf are the distinctive words of a call, for the contrast. It reads the
// call as written, not its shape: the shape has already collapsed the very
// differences — a separator, a flag — the contrast exists to name.
func tokensOf(text string) []string {
	seen := map[string]bool{}
	tokens := []string{}
	for _, token := range strings.Fields(text) {
		token = strings.Trim(token, `",;`)
		if len(token) < 2 || strings.ContainsAny(token, `/\`) || isNumeric(token) {
			continue
		}
		if seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	return tokens
}

// contrast says what distinguishes the calls that failed from the ones that
// worked: the tokens present in one side and absent from the other. It is
// grouping, not judgement.
func contrast(successful, failing map[string]int, failures, successes int) string {
	onlyFailing, onlySuccessful := []string{}, []string{}
	for token := range failing {
		if successful[token] == 0 {
			onlyFailing = append(onlyFailing, token)
		}
	}
	for token := range successful {
		if failing[token] == 0 {
			onlySuccessful = append(onlySuccessful, token)
		}
	}
	sort.Strings(onlyFailing)
	sort.Strings(onlySuccessful)
	trim := func(values []string) string {
		if len(values) > 6 {
			values = append(values[:6], "…")
		}
		return strings.Join(values, " ")
	}
	switch {
	case failures == 0:
		return "every call worked"
	case successes == 0:
		return "every call failed"
	case len(onlyFailing) == 0 && len(onlySuccessful) == 0:
		return "the failures and the successes are written the same way; the difference is not in the call text"
	default:
		return strings.TrimSpace("only in the failures: " + trim(onlyFailing) + "; only in the successes: " + trim(onlySuccessful))
	}
}

// ReportText renders the clusters the operator reads. Nothing is promoted and
// nothing is created: tool creation is operator-initiated and is not in this
// contract.
func ReportText(clusters []Cluster, limit int) string {
	lines := []string{"Tool candidates, ranked by frequency × failure rate.", ""}
	shown := 0
	for _, cluster := range clusters {
		if cluster.Failures == 0 {
			continue
		}
		if limit > 0 && shown >= limit {
			break
		}
		shown++
		lines = append(lines,
			fmt.Sprintf("%d. %s · %s", shown, cluster.Tool, cluster.Shape),
			fmt.Sprintf("   %d calls, %d failed (%.0f%%), score %.1f", cluster.Count, cluster.Failures, cluster.FailureRate*100, cluster.Score),
		)
		if len(cluster.FailureCauses) > 0 {
			lines = append(lines, "   causes: "+strings.Join(cluster.FailureCauses, "; "))
		}
		lines = append(lines, "   contrast: "+cluster.Contrast)
		for _, example := range cluster.Representatives {
			if len(example) > 120 {
				example = example[:120] + "…"
			}
			lines = append(lines, "   · "+example)
		}
		lines = append(lines, "")
	}
	if shown == 0 {
		lines = append(lines, "No cluster failed; nothing is a candidate.")
	}
	return strings.Join(lines, "\n")
}

// isNumeric keeps bare numbers out of the contrast.
func isNumeric(token string) bool {
	for _, r := range token {
		if (r < '0' || r > '9') && r != '.' && r != ',' && r != '-' {
			return false
		}
	}
	return true
}
