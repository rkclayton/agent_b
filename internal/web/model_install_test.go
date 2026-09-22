package web

import (
	"context"
	"testing"

	"harness/internal/config"
)

func TestModelInstallMemoryUsesServerDetectionForBackend(t *testing.T) {
	server := &Server{cfg: &config.Config{Shell: config.Shell{ServiceAccount: config.ShellServiceAccount{Account: "fixture"}}}}
	server.detectLocal = func(context.Context, string) (any, error) {
		return map[string]any{
			"system_memory_bytes": uint64(32 << 30),
			"gpus": []any{
				map[string]any{"vendor": "Intel", "vram_bytes": uint64(2 << 30)},
				map[string]any{"vendor": "NVIDIA", "vram_bytes": uint64(4 << 30)},
			},
		}, nil
	}
	for backend, want := range map[string]uint64{"cpu": 32 << 30, "vulkan": 2 << 30, "cuda": 4 << 30} {
		got, err := server.modelInstallMemory(context.Background(), backend)
		if err != nil || got != want {
			t.Fatalf("%s got=%d err=%v want=%d", backend, got, err, want)
		}
	}
}
