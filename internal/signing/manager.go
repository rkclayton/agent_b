package signing

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("code signing is supported only on Windows")

type Request struct {
	Thumbprint   string
	TimestampURL string
	ExpectedHash string
	ProcessID    int
	PFX          []byte
	Password     []byte
}

type FileStatus struct {
	Path        string `json:"path"`
	Status      string `json:"status"`
	Signer      string `json:"signer"`
	Thumbprint  string `json:"thumbprint"`
	Timestamped bool   `json:"timestamped"`
	TimestampBy string `json:"timestamp_by,omitempty"`
}

type Certificate struct {
	Thumbprint string `json:"thumbprint"`
	Subject    string `json:"subject"`
	HasKey     bool   `json:"has_private_key"`
	Store      string `json:"store,omitempty"`
}

type Status struct {
	Supported bool `json:"supported"`
	CanManage bool `json:"can_manage"`
	// AdminState is item 2gj's three token states: "elevated",
	// "not_elevated", "not_admin", or "unknown" when membership could not be
	// read. An unelevated administrator is still an administrator, and the
	// readout says signing needs an elevated run rather than refusing him.
	AdminState     string        `json:"admin_state"`
	Configured     bool          `json:"configured"`
	Thumbprint     string        `json:"thumbprint"`
	Subject        string        `json:"subject"`
	HasKey         bool          `json:"has_private_key"`
	CodeSigningEKU bool          `json:"code_signing_eku"`
	ChainValid     bool          `json:"chain_valid"`
	Files          []FileStatus  `json:"files"`
	Certificates   []Certificate `json:"certificates"`
}

type Result struct {
	Thumbprint string `json:"thumbprint"`
	Subject    string `json:"subject"`
	Message    string `json:"message"`
}

type Manager interface {
	Status(context.Context, Request) (Status, error)
	Create(context.Context, Request) (Result, error)
	Import(context.Context, Request) (Result, error)
	Select(context.Context, Request) (Result, error)
	Export(context.Context, Request) ([]byte, error)
	Sign(context.Context, Request) (Result, error)
}
