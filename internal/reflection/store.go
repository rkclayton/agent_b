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
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	SessionID string    `json:"session_id"`
	RunID     string    `json:"run_id"`
	Workspace string    `json:"workspace"`
	PlanID    string    `json:"plan_id"`
	Profile   string    `json:"profile"`
	// Aux is false when the summary came from the run's own profile because no
	// aux profile is configured; the text is marked the same way.
	Aux      bool     `json:"aux"`
	Read     []string `json:"read"`
	Written  []string `json:"written"`
	Changed  string   `json:"changed"`
	Open     string   `json:"open"`
	Text     string   `json:"text"`
	Failed   string   `json:"failed,omitempty"`
	Duration int64    `json:"duration_ms"`
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
			profile TEXT NOT NULL,
			aux INTEGER NOT NULL,
			read_files TEXT NOT NULL,
			written_files TEXT NOT NULL,
			changed TEXT NOT NULL,
			open TEXT NOT NULL,
			text TEXT NOT NULL,
			failed TEXT NOT NULL,
			duration_ms INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS summaries_at ON summaries (at)`,
		`CREATE TABLE IF NOT EXISTS overviews (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			plan_id TEXT NOT NULL,
			tier TEXT NOT NULL,
			text TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS overviews_plan ON overviews (plan_id, at)`,
		`CREATE TABLE IF NOT EXISTS reports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			at INTEGER NOT NULL,
			text TEXT NOT NULL,
			clusters TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
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
		`INSERT INTO summaries (at, session_id, run_id, workspace, plan_id, profile, aux, read_files, written_files, changed, open, text, failed, duration_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		summary.At.UTC().UnixMilli(), summary.SessionID, summary.RunID, summary.Workspace, summary.PlanID, summary.Profile,
		boolToInt(summary.Aux), joined(summary.Read), joined(summary.Written), summary.Changed, summary.Open, summary.Text, summary.Failed, summary.Duration)
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
	query := `SELECT id, at, session_id, run_id, workspace, plan_id, profile, aux, read_files, written_files, changed, open, text, failed, duration_ms
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
		var aux int
		var read, written string
		if err := rows.Scan(&summary.ID, &at, &summary.SessionID, &summary.RunID, &summary.Workspace, &summary.PlanID, &summary.Profile, &aux, &read, &written, &summary.Changed, &summary.Open, &summary.Text, &summary.Failed, &summary.Duration); err != nil {
			return nil, fmt.Errorf("read summaries: %w", err)
		}
		summary.At = time.UnixMilli(at).UTC()
		summary.Aux = aux == 1
		summary.Read, summary.Written = split(read), split(written)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// Prune drops summaries older than the retention window. Overviews and reports
// are kept: they are the diffable record.
func (s *Store) Prune(now time.Time) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM summaries WHERE at < ?`, now.Add(-Retention).UTC().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("prune summaries: %w", err)
	}
	return result.RowsAffected()
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
