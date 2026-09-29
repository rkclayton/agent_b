// Package broker is this AgentB's client for the broker in its own repository:
// it dials out, holds one session, and carries application messages to and from
// the operator's phone. Item 2kq.
//
// NOTHING HERE TRUSTS THE BROKER. The broker routes ciphertext and sees metadata;
// every value below is derived from the two endpoints' own keys, and the wire
// documents under docs/external are normative. The vectors in
// docs/external/broker-protocol-v2-vectors.json are re-derived by this package's
// own gate, both roles, before anything else was written.
package broker

// The stubs this file declares are what the vector gate calls. They are written
// before their bodies on purpose: the gate is red first.
