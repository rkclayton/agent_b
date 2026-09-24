//go:build !windows

package serviceaccount

import "context"

type unsupportedManager struct{}

func New(string) Manager { return unsupportedManager{} }

func (unsupportedManager) Status(context.Context, string) (Status, error) {
	return Status{Supported: false}, nil
}

func (unsupportedManager) Setup(context.Context, string, string, bool, *Protection) (SetupResult, error) {
	return SetupResult{}, ErrUnsupported
}
