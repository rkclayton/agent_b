// Command progress-replay prints the stuck-run detectors' records for a session
// journal.
//
// It reads a journal as it is stored — JSONL, one record per line — either from
// a named file or on standard input, and it also accepts a JSON array of events
// because a gate feeds it one. A line it cannot parse is reported with its line
// number and skipped, and the unreadable lines are summarized at the end, so a
// journal that went wrong can still be diagnosed (item 2lv).
//
//	progress-replay path/to/session.jsonl
//	progress-replay < path/to/session.jsonl
package main
