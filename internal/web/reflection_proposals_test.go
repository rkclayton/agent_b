package web

import (
	"testing"
	"time"
)

func TestReflectionProposesAPlanAndApprovalRegistersItThroughTheExistingPath(t *testing.T) {
	returned := make(chan struct{})
	go func() {
		(&Server{}).OfferReflectionProposals()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("reflection waited to offer a planning approval")
	}
}

func TestADeclinedProposalIsNotOfferedAgainUntilTheAgentFilesChange(t *testing.T) {
	for range 2 {
		(&Server{}).OfferReflectionProposals()
	}
}
