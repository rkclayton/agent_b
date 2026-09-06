//go:build !windows

package signing

import "context"

type unsupportedManager struct{}

func New(string) Manager { return unsupportedManager{} }
func (unsupportedManager) Status(context.Context, Request) (Status, error) {
	return Status{}, ErrUnsupported
}
func (unsupportedManager) Create(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}
func (unsupportedManager) Import(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}
func (unsupportedManager) Select(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}
func (unsupportedManager) Export(context.Context, Request) ([]byte, error) {
	return nil, ErrUnsupported
}
func (unsupportedManager) Sign(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}
