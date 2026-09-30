//go:build windows

package serviceaccount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

func TestStatusUsesReadOnlyNativeInspection(t *testing.T) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		t.Fatal(err)
	}
	account := "agentb-inspect-" + hex.EncodeToString(bytes)
	manager := NewNative()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := manager.Status(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Supported || status.Account != account || status.Exists {
		t.Fatalf("unexpected status: %+v", status)
	}
}
