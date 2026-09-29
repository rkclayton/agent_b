package hardening

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("Agent_b host hardening is supported only on Windows")

type Request struct {
	AccountName          string
	ApplicationDirectory string
	DataDirectory        string
	WorkspaceDirectory   string
	ExchangeDirectory    string
	AllowLocalNetwork    bool
	LocalSubnets         []string
	AllowedModelRanges   []string
}

type ComponentStatus struct {
	Supported         bool        `json:"supported"`
	AccountExists     bool        `json:"account_exists"`
	Applied           bool        `json:"applied"`
	Drift             int         `json:"drift,omitempty"`
	Summary           string      `json:"summary"`
	ResolutionChanged bool        `json:"resolution_changed,omitempty"`
	Items             []DriftItem `json:"items,omitempty"`
}

type DriftItem struct {
	Path     string `json:"path,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Expected string `json:"expected"`
	Found    string `json:"found"`
}

type Status struct {
	Supported             bool            `json:"supported"`
	HarnessElevated       bool            `json:"harness_elevated"`
	ACL                   ComponentStatus `json:"acl"`
	Firewall              ComponentStatus `json:"firewall"`
	Applied               bool            `json:"applied"`
	AllowLocalNetwork     bool            `json:"allow_local_network"`
	ConfirmedLocalSubnets []string        `json:"confirmed_local_subnets"`
	AllowedModelRanges    []string        `json:"allowed_model_ranges"`
	DetectedLocalSubnets  []string        `json:"detected_local_subnets,omitempty"`
}

type RunResult struct {
	Attempted bool
}

type Manager interface {
	Status(context.Context, Request) (Status, error)
	Run(context.Context, string, Request) (RunResult, error)
	GrantPlan(context.Context, string, string) error
}
