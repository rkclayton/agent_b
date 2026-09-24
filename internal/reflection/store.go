// Package reflection is the pass that runs after a chat's run closes and on a
// daily tick (item 17-i). It records one summary per run, extracts the shape of
// a repository from the code rather than from history, joins the two into a
// text overview per plan, and mines recorded tool calls for the tool-candidate
// report. It never touches the run loop's latency: everything here happens
// after a run is over, and a failure of any part leaves the run untouched.
package reflection

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Retention is the window automatic reflection reads and the store keeps for
// summaries. Overviews and reports are kept whole so they stay diffable
// (decided in the item before building).
const Retention = 30 * 24 * time.Hour

// Summary is one run's account of itself.
type Summary struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	SessionID  string    `json:"session_id"`
	RunID      string    `json:"run_id"`
	Workspace  string    `json:"workspace"`
	PlanID     string    `json:"plan_id"`
	Connection string    `json:"connection"`
	// Aux is false when the summary came from the run's own connection because no
	// aux connection is configured; the text is marked the same way.
	Aux      bool     `json:"aux"`
	Read     []string `json:"read"`
	Written  []string `json:"written"`
	Changed  string   `json:"changed"`
	Open     string   `json:"open"`
	Text     string   `json:"text"`
	Failed   string   `json:"failed,omitempty"`
	Duration int64    `json:"duration_ms"`
	// Untrusted marks a run that read content the operator did not write — a
	// fetched page, an untrusted tool result. Reflection writes no memory note
	// from such a run (v1.1.0/W6 cold review).
	Untrusted bool `json:"untrusted"`
}

// Overview is one reflection pass's text for one plan, kept so two passes can
// be diffed.
type Overview struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	PlanID string    `json:"plan_id"`
	Tier   string    `json:"tier"`
	Text   string    `json:"text"`
}

// Report is the tool-candidate report: the text the operator reads and the
// clusters behind it.
type Report struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Text     string    `json:"text"`
	Clusters []Cluster `json:"clusters"`
}

// Store is the SQLite store under the data root. modernc.org/sqlite is pure
// Go, needs no cgo, and carries a permissive licence, which is what item 17-i
// asks of the driver.
type Store struct{ db *sql.DB }

// Open creates or opens <dataRoot>/reflection/reflection.db.
func Open(dataRoot string) (*Store, error) {
	dir := filepath.Join(dataRoot, "reflection")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("reflection store directory: %w", err)
	}
	return OpenAt(filepath.Join(dir, "reflection.db"))
}

// OpenAt opens one database file.
func OpenAt(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("reflection store: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			session_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			workspace TEXT NOT NULL,
			plan_id TEXT NOT NULL,
			connection TEXT NOT NULL,
			aux INTEGER NOT NULL,
			read_files TEXT NOT NULL,
			written_files TEXT NOT NULL,
			changed TEXT NOT NULL,
			open TEXT NOT NULL,
			text TEXT NOT NULL,
			failed TEXT NOT NULL,
			duration_ms INTEGER NOT NULL,
			untrusted INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS summaries_at ON summaries (at)`,
		// A store written before v1.1.0/W6 has no untrusted column.
		`ALTER TABLE summaries ADD COLUMN untrusted INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS overviews (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			plan_id TEXT NOT NULL,
			tier TEXT NOT NULL,
			text TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS overviews_plan ON overviews (plan_id, at)`,
		`CREATE TABLE IF NOT EXISTS proposals (
			root TEXT PRIMARY KEY,
			agent_file TEXT NOT NULL,
			activity TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			state TEXT NOT NULL,
			at INTEGER NOT NULL,
			decided_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			text TEXT NOT NULL,
			clusters TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			// An ALTER that has already been applied is not a failure.
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("reflection schema: %w", err)
		}
	}
	return nil
}

func joined(values []string) string { return strings.Join(values, "\n") }
func split(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, "\n")
}

// PutSummary records one run's summary.
func (s *Store) PutSummary(summary Summary) (int64, error) {
	if summary.At.IsZero() {
		summary.At = time.Now().UTC()
	}
	result, err := s.db.Exec(
		`INSERT INTO summaries (at, session_id, run_id, workspace, plan_id, connection, aux, read_files, written_files, changed, open, text, failed, duration_ms, untrusted)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		summary.At.UTC().UnixMilli(), summary.SessionID, summary.RunID, summary.Workspace, summary.PlanID, summary.Connection,
		boolToInt(summary.Aux), joined(summary.Read), joined(summary.Written), summary.Changed, summary.Open, summary.Text, summary.Failed, summary.Duration, boolToInt(summary.Untrusted))
	if err != nil {
		return 0, fmt.Errorf("record summary: %w", err)
	}
	return result.LastInsertId()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Summaries returns the summaries at or after since, newest first. A zero
// since reads them all, which is what a manual reflection pass does.
func (s *Store) Summaries(since time.Time, limit int) ([]Summary, error) {
	query := `SELECT id, at, session_id, run_id, workspace, plan_id, connection, aux, read_files, written_files, changed, open, text, failed, duration_ms, untrusted
		FROM summaries`
	args := []any{}
	if !since.IsZero() {
		query += ` WHERE at >= ?`
		args = append(args, since.UTC().UnixMilli())
	}
	query += ` ORDER BY at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read summaries: %w", err)
	}
	defer rows.Close()
	summaries := []Summary{}
	for rows.Next() {
		var summary Summary
		var at int64
		var aux, untrusted int
		var read, written string
		if err := rows.Scan(&summary.ID, &at, &summary.SessionID, &summary.RunID, &summary.Workspace, &summary.PlanID, &summary.Connection, &aux, &read, &written, &summary.Changed, &summary.Open, &summary.Text, &summary.Failed, &summary.Duration, &untrusted); err != nil {
			return nil, fmt.Errorf("read summaries: %w", err)
		}
		summary.At = time.UnixMilli(at).UTC()
		summary.Aux, summary.Untrusted = aux == 1, untrusted == 1
		summary.Read, summary.Written = split(read), split(written)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// KeptOverviews and KeptReports bound the diffable record. Without a bound the
// store grows with every pass forever (v1.1.0/W6 cold review).
const (
	KeptOverviews = 60
	KeptReports   = 30
)

// Prune drops summaries older than the retention window, and keeps the newest
// overviews per plan and the newest reports.
func (s *Store) Prune(now time.Time) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM summaries WHERE at < ?`, now.Add(-Retention).UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("prune summaries: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`DELETE FROM overviews WHERE id NOT IN (
			SELECT id FROM overviews o WHERE (SELECT COUNT(*) FROM overviews n WHERE n.plan_id = o.plan_id AND n.id >= o.id) <= ?
		)`, KeptOverviews); err != nil {
		return removed, fmt.Errorf("prune overviews: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM reports WHERE id NOT IN (SELECT id FROM reports ORDER BY id DESC LIMIT ?)`, KeptReports); err != nil {
		return removed, fmt.Errorf("prune reports: %w", err)
	}
	return removed, nil
}

// PutOverview stores one pass's overview for a plan.
func (s *Store) PutOverview(overview Overview) (int64, error) {
	if overview.At.IsZero() {
		overview.At = time.Now().UTC()
	}
	result, err := s.db.Exec(`INSERT INTO overviews (at, plan_id, tier, text) VALUES (?,?,?,?)`,
		overview.At.UTC().UnixMilli(), overview.PlanID, overview.Tier, overview.Text)
	if err != nil {
		return 0, fmt.Errorf("record overview: %w", err)
	}
	return result.LastInsertId()
}

// Overviews returns a plan's overviews, newest first.
func (s *Store) Overviews(planID string, limit int) ([]Overview, error) {
	query := `SELECT id, at, plan_id, tier, text FROM overviews`
	args := []any{}
	if planID != "" {
		query += ` WHERE plan_id = ?`
		args = append(args, planID)
	}
	query += ` ORDER BY at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read overviews: %w", err)
	}
	defer rows.Close()
	overviews := []Overview{}
	for rows.Next() {
		var overview Overview
		var at int64
		if err := rows.Scan(&overview.ID, &at, &overview.PlanID, &overview.Tier, &overview.Text); err != nil {
			return nil, fmt.Errorf("read overviews: %w", err)
		}
		overview.At = time.UnixMilli(at).UTC()
		overviews = append(overviews, overview)
	}
	return overviews, rows.Err()
}

// PutReport stores the tool-candidate report.
func (s *Store) PutReport(report Report) (int64, error) {
	if report.At.IsZero() {
		report.At = time.Now().UTC()
	}
	clusters, err := json.Marshal(report.Clusters)
	if err != nil {
		return 0, fmt.Errorf("record report: %w", err)
	}
	result, err := s.db.Exec(`INSERT INTO reports (at, text, clusters) VALUES (?,?,?)`, report.At.UTC().UnixMilli(), report.Text, string(clusters))
	if err != nil {
		return 0, fmt.Errorf("record report: %w", err)
	}
	return result.LastInsertId()
}

// LatestReport is the newest tool-candidate report, or a zero Report when
// reflection has not produced one yet.
func (s *Store) LatestReport() (Report, error) {
	row := s.db.QueryRow(`SELECT id, at, text, clusters FROM reports ORDER BY at DESC, id DESC LIMIT 1`)
	var report Report
	var at int64
	var clusters string
	switch err := row.Scan(&report.ID, &at, &report.Text, &clusters); {
	case err == sql.ErrNoRows:
		return Report{}, nil
	case err != nil:
		return Report{}, fmt.Errorf("read report: %w", err)
	}
	report.At = time.UnixMilli(at).UTC()
	if err := json.Unmarshal([]byte(clusters), &report.Clusters); err != nil {
		return report, fmt.Errorf("read report clusters: %w", err)
	}
	return report, nil
}

// Plan proposals (item 17-i's registration decision, v1.1.1/W3). Reflection
// never registers a plan: it proposes one, the operator answers the existing
// card, and the answer is remembered. A declined proposal stays declined until
// the repository's agent files change — the fingerprint below — so the
// operator is not asked the same question every day.
type Proposal struct {
	Root        string    `json:"root"`
	AgentFile   string    `json:"agent_file"`
	Activity    string    `json:"activity"`
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	At          time.Time `json:"at"`
	DecidedAt   time.Time `json:"decided_at,omitempty"`
}

// Proposal states.
const (
	ProposalPending  = "pending"
	ProposalOffered  = "offered"
	ProposalApproved = "approved"
	ProposalDeclined = "declined"
)

// UpsertProposal records a proposal. A root already approved is left alone; a
// declined one returns only when its fingerprint changes.
func (s *Store) UpsertProposal(proposal Proposal) error {
	if proposal.At.IsZero() {
		proposal.At = time.Now().UTC()
	}
	row := s.db.QueryRow(`SELECT state, fingerprint FROM proposals WHERE root = ?`, proposal.Root)
	var state, fingerprint string
	switch err := row.Scan(&state, &fingerprint); {
	case err == sql.ErrNoRows:
		_, err := s.db.Exec(`INSERT INTO proposals (root, agent_file, activity, fingerprint, state, at, decided_at) VALUES (?,?,?,?,?,?,0)`,
			proposal.Root, proposal.AgentFile, proposal.Activity, proposal.Fingerprint, ProposalPending, proposal.At.UTC().UnixMilli())
		return err
	case err != nil:
		return fmt.Errorf("read proposal: %w", err)
	case state == ProposalApproved:
		return nil
	case state == ProposalDeclined && fingerprint == proposal.Fingerprint:
		return nil
	}
	_, err := s.db.Exec(`UPDATE proposals SET agent_file = ?, activity = ?, fingerprint = ?, state = ?, at = ? WHERE root = ?`,
		proposal.AgentFile, proposal.Activity, proposal.Fingerprint, ProposalPending, proposal.At.UTC().UnixMilli(), proposal.Root)
	return err
}

// PendingProposals are the ones the operator has not answered.
func (s *Store) PendingProposals(limit int) ([]Proposal, error) {
	query := `SELECT root, agent_file, activity, fingerprint, state, at FROM proposals WHERE state = ? ORDER BY at ASC`
	args := []any{ProposalPending}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("read proposals: %w", err)
	}
	defer rows.Close()
	proposals := []Proposal{}
	for rows.Next() {
		var proposal Proposal
		var at int64
		if err := rows.Scan(&proposal.Root, &proposal.AgentFile, &proposal.Activity, &proposal.Fingerprint, &proposal.State, &at); err != nil {
			return nil, fmt.Errorf("read proposals: %w", err)
		}
		proposal.At = time.UnixMilli(at).UTC()
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

// SetProposalState records what happened to a proposal.
func (s *Store) SetProposalState(root, state string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.Exec(`UPDATE proposals SET state = ?, decided_at = ? WHERE root = ?`, state, at.UTC().UnixMilli(), root)
	return err
}

// ProposalFor reads one proposal, or false when the root has none.
func (s *Store) ProposalFor(root string) (Proposal, bool, error) {
	row := s.db.QueryRow(`SELECT root, agent_file, activity, fingerprint, state, at FROM proposals WHERE root = ?`, root)
	var proposal Proposal
	var at int64
	switch err := row.Scan(&proposal.Root, &proposal.AgentFile, &proposal.Activity, &proposal.Fingerprint, &proposal.State, &at); {
	case err == sql.ErrNoRows:
		return Proposal{}, false, nil
	case err != nil:
		return Proposal{}, false, fmt.Errorf("read proposal: %w", err)
	}
	proposal.At = time.UnixMilli(at).UTC()
	return proposal, true, nil
}
