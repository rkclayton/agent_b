package probe

import (
	"context"
	"os"
	"testing"
	"time"

	"harness/internal/config"
)

func TestLiveDiscoverOperatorTypedHost(t *testing.T) {
	host := os.Getenv("AGENTB_DISCOVERY_LIVE_HOST")
	if host == "" {
		t.Skip("AGENTB_DISCOVERY_LIVE_HOST is not set")
	}
	profile := config.Defaults(t.TempDir()).Servers[0]
	profile.BaseURL = host
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := DiscoverEndpoint(ctx, &profile)
	for _, attempt := range result.Attempts {
		t.Logf("probe.request guard=operator_typed_host_only allowed=%t base_url=%s result=%s", attempt.Allowed, attempt.BaseURL, attempt.Result)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("found base_url=%s models=%v", result.BaseURL, result.Models)
}
