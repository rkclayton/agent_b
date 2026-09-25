//go:build windows

package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"harness/internal/events"
)

func TestVAPIDStoreRetryPayloadAndGoneCleanup(t *testing.T) {
	statuses := []int{500, 201, 410}
	calls := 0
	var authorization, encoding string
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		authorization = r.Header.Get("Authorization")
		encoding = r.Header.Get("Content-Encoding")
		w.WriteHeader(statuses[calls-1])
	}))
	defer service.Close()
	client, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	sub := Subscription{Endpoint: service.URL}
	sub.Keys.P256DH = base64.RawURLEncoding.EncodeToString(client.PublicKey().Bytes())
	sub.Keys.Auth = base64.RawURLEncoding.EncodeToString(auth)
	root := t.TempDir()
	manager := New(root, nil, func(string) string { return "Private chat" })
	manager.wait = func(context.Context, time.Duration) error { return nil }
	if _, err := manager.Enable(true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Add(sub); err != nil {
		t.Fatal(err)
	}
	event := events.New(events.ApprovalRequired, "s9", "", map[string]any{"name": "shell", "text": "SECRET CHAT CONTENT"})
	payload := manager.payload(event)
	if strings.Contains(strings.Join([]string{payload["title"], payload["body"], payload["url"]}, " "), "SECRET") || payload["title"] != "Agent_b · Private chat" {
		t.Fatalf("payload=%v", payload)
	}
	if err := manager.Deliver(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || encoding != "aes128gcm" || !strings.HasPrefix(authorization, "vapid t=") {
		t.Fatalf("calls=%d encoding=%q auth=%q", calls, encoding, authorization)
	}
	if raw, err := os.ReadFile(manager.key.Path()); err != nil || len(raw) == 32 {
		t.Fatalf("DPAPI key bytes=%d err=%v", len(raw), err)
	}
	if err := manager.Deliver(context.Background(), event); err == nil {
		t.Fatal("410 should report delivery failure")
	}
	if manager.State().Subscriptions != 0 {
		t.Fatal("410 subscription was not removed")
	}
}
