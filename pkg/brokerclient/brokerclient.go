// Package brokerclient is the one import another repository uses to speak to the broker
// (item 2wp). It re-exports internal/broker's device client, the agent client's public
// surface and the payload types the phone's core builds; everything else stays internal.
package brokerclient

import "harness/internal/broker"

type (
	Client          = broker.Client
	CiphertextFrame = broker.CiphertextFrame
	Counters        = broker.Counters
	DeviceClient    = broker.DeviceClient
	Dialer          = broker.Dialer
	ErrorPayload    = broker.ErrorPayload
	Frame           = broker.Frame
	Identity        = broker.Identity
	IDOnlyFrame     = broker.IDOnlyFrame
	Pairing         = broker.Pairing
	PairingOffer    = broker.PairingOffer
	PairPeerPayload = broker.PairPeerPayload
	Problem         = broker.Problem
	Session         = broker.Session
	SessionFrame    = broker.SessionFrame
	Transport       = broker.Transport
)

const (
	SendFrameRate = broker.SendFrameRate

	DirectionAgentToDevice = broker.DirectionAgentToDevice
	DirectionDeviceToAgent = broker.DirectionDeviceToAgent

	FrameHello         = broker.FrameHello
	FrameChallenge     = broker.FrameChallenge
	FrameAuth          = broker.FrameAuth
	FrameReady         = broker.FrameReady
	FramePairBegin     = broker.FramePairBegin
	FramePairWaiting   = broker.FramePairWaiting
	FramePairPeer      = broker.FramePairPeer
	FramePairConfirm   = broker.FramePairConfirm
	FramePairComplete  = broker.FramePairComplete
	FrameSessionInit   = broker.FrameSessionInit
	FrameSessionResp   = broker.FrameSessionResp
	FrameSessionFinish = broker.FrameSessionFinish
	FrameSessionReady  = broker.FrameSessionReady
	FrameCiphertext    = broker.FrameCiphertext
	FrameAck           = broker.FrameAck
	FrameQueued        = broker.FrameQueued
	FrameResend        = broker.FrameResend
	FrameExpired       = broker.FrameExpired
	FramePing          = broker.FramePing
	FramePong          = broker.FramePong
	FrameError         = broker.FrameError
)

var (
	NewDeviceClient = broker.NewDeviceClient
	NewClient       = broker.NewClient
	Dial            = broker.Dial
	BeginPairing    = broker.BeginPairing
	NewPairingCode  = broker.NewPairingCode
	PairingLink     = broker.PairingLink
	ReadPairingLink = broker.ReadPairingLink
	Fingerprint     = broker.Fingerprint
	KeyIDFor        = broker.KeyIDFor
	TranscriptHash  = broker.TranscriptHash
	SignAuth        = broker.SignAuth
	NewResponder    = broker.NewResponder
	SessionPrologue = broker.SessionPrologue
	TransportAAD    = broker.TransportAAD
	Encode          = broker.Encode
	Decode          = broker.Decode
	DecodeInto      = broker.DecodeInto
)
