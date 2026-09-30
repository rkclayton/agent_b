package identity

import "context"

// TokenProvider is the connector-facing part of an interactive identity provider.
// Origin is deliberately separate so destination enforcement happens before Token.
type TokenProvider interface {
	Origin(string) (string, error)
	Token(context.Context, string) (string, error)
}

// Provider is the operator-facing extension. Settings is generic over this contract;
// adding another interactive provider does not add another sign-in code path there.
type Provider interface {
	TokenProvider
	SignIn(context.Context, string) (string, error)
	DeviceCode(context.Context, string) (DeviceAuthorization, error)
	Account(context.Context, string) (string, error)
	SignOut(string) error
}

type DeviceAuthorization struct {
	UserCode        string
	VerificationURL string
	Message         string
	Wait            func(context.Context) (string, error)
}
