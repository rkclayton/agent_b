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
}

type SetupResult struct {
	// Attempted is true once the elevated setup script started. A failure after
	// that point can be a partial account/password change and must not be
	// reported as a harmless UAC cancellation.
	Attempted bool
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
