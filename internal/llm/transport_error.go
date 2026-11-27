package llm

import (
	"errors"
	"io"
	"net"
)

type TransportKind string

const (
	TransportDial      TransportKind = "dial"
	TransportConnected TransportKind = "connected"
	TransportStream    TransportKind = "stream"
)

type TransportError struct {
	Kind TransportKind
	Err  error
}

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

func TransportKindOf(err error) TransportKind {
	var transport *TransportError
	if errors.As(err, &transport) {
		return transport.Kind
	}
	return ""
}

func streamReadError(err error) error {
	var networkError net.Error
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &networkError) {
		return &TransportError{Kind: TransportStream, Err: err}
	}
	return err
}
