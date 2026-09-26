package serviceaccount

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("service-account setup is supported only on Windows")

type Status struct {
	Supported        bool   `json:"supported"`
	Account          string `json:"account"`
	Exists           bool   `json:"exists"`
	Enabled          bool   `json:"enabled"`
	Administrator    bool   `json:"administrator"`
	UsersMember      bool   `json:"users_member"`
	LockedOut        bool   `json:"locked_out"`
	HarnessElevated  bool   `json:"harness_elevated"`
	State            string `json:"state,omitempty"`
	Action           string `json:"action,omitempty"`
	CredentialStored bool   `json:"credential_stored,omitempty"`
	// Item 2li (e): a machine-provisioned identity must read as ready without
	// offering to redo it. "machine" is how Settings knows not to.
	CredentialScope string `json:"credential_scope,omitempty"`
}

// Item 2kk (e): a launch that never happened is its own state, distinct from a
// run that failed. UAC declined, or policy refused to elevate, is not an
// account operation gone wrong -- nothing was touched and nothing needs
// repairing, and the operator should be told that and not sent to inspect an
// account that was never opened.
type LaunchOutcome string

const (
	LaunchDeclined LaunchOutcome = "declined"
	LaunchStarted  LaunchOutcome = "started"
)

// Item 2kk (a): the result the elevated child WROTE, not text scraped from a
// stream. Ok and Message come from a small JSON file the child is told to
// write; LogPath names where the full streams were kept, so (d) can show a
// complete message and still point at everything else.
type ElevatedResult struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message"`
	Outcome string `json:"outcome,omitempty"`
	Account string `json:"account,omitempty"`
}

type SetupResult struct {
	// Attempted is true once the elevated setup script started. A failure after
	// that point can be a partial account/password change and must not be
	// reported as a harmless UAC cancellation.
	Attempted bool
	// Launch says whether the elevated process ever started. (e).
	Launch LaunchOutcome
	// Result is what the child wrote, when it wrote one.
	Result *ElevatedResult
	// LogPath is where the launcher's own streams were kept, named in the
	// message the operator sees so the detail is one click away. (d).
	LogPath string
}

type Protection struct {
	ApplicationDirectory string
	DataDirectory        string
	WorkspaceDirectory   string
	ExchangeDirectory    string
	ModelAddress         string
	ModelPort            int
	AllowLocalNetwork    bool
	LocalSubnets         []string
	AllowedModelRanges   []string
}

type Manager interface {
	Status(context.Context, string) (Status, error)
	Setup(context.Context, string, string, bool, *Protection) (SetupResult, error)
}
