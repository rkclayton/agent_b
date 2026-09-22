package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"harness/internal/config"
	"harness/internal/events"
	"harness/internal/modelinstall"
)

var pinnedLlamaRuntime = modelinstall.Runtime{
	Version: "b10964",
	Source:  "https://github.com/ggml-org/llama.cpp/releases/tag/b10964",
	Backend: map[string][]modelinstall.Artifact{
		"cpu":    {{Name: "llama-b10964-bin-win-cpu-x64.zip", Bytes: 18427629, SHA256: "917f39c076402c421224824607397af20f53625a60defc20e8dd22446bf4c5d7", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b10964/llama-b10964-bin-win-cpu-x64.zip"}},
		"vulkan": {{Name: "llama-b10964-bin-win-vulkan-x64.zip", Bytes: 31674542, SHA256: "1ee3ad952f4ba71f438bd6d7bebef19e1c7af04adcaa35d08b4ddabb27d4c642", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b10964/llama-b10964-bin-win-vulkan-x64.zip"}},
		"cuda": {
			{Name: "llama-b10964-bin-win-cuda-12.4-x64.zip", Bytes: 254067651, SHA256: "264f20d7ee3860aecca9ec12418357a9f3e80349a2b186f66c63859ded1a9593", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b10964/llama-b10964-bin-win-cuda-12.4-x64.zip"},
			{Name: "cudart-llama-bin-win-cuda-12.4-x64.zip", Bytes: 391443627, SHA256: "8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6", URL: "https://github.com/ggml-org/llama.cpp/releases/download/b10964/cudart-llama-bin-win-cuda-12.4-x64.zip"},
		},
	},
}

func modelArtifact(repo, file, hash string, size int64) modelinstall.Artifact {
	return modelinstall.Artifact{Name: file, Bytes: size, SHA256: hash, URL: "https://huggingface.co/" + repo + "/resolve/main/" + file + "?download=true"}
}

var pinnedModels = []modelinstall.Model{
	{ID: "qwen3.5-0.8b-q4km", Label: "Qwen3.5 0.8B Q4_K_M", MinGiB: 0, MaxGiB: 3, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-0.8B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-0.8B-GGUF", "Qwen_Qwen3.5-0.8B-Q4_K_M.gguf", "fb044e93939a70469c905781334f5de1e6c8b608ced6cbc8c9249bd4127d9526", 579615840)},
	{ID: "qwen3.5-2b-q4km", Label: "Qwen3.5 2B Q4_K_M", MinGiB: 4, MaxGiB: 5, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-2B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-2B-GGUF", "Qwen_Qwen3.5-2B-Q4_K_M.gguf", "57a1085840f497d764a7fc5d346922dbde961efb54cc792ea81d694fd846a1d8", 1396198496)},
	{ID: "qwen3.5-4b-q4km", Label: "Qwen3.5 4B Q4_K_M", MinGiB: 6, MaxGiB: 11, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-4B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-4B-GGUF", "Qwen_Qwen3.5-4B-Q4_K_M.gguf", "13c16f426047e2de38cd075bdade4a7bcbc8c774384876f677740cda65f8a983", 3013027808)},
	{ID: "qwen3.5-9b-q4km", Label: "Qwen3.5 9B Q4_K_M", MinGiB: 12, MaxGiB: 19, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-9B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-9B-GGUF", "Qwen_Qwen3.5-9B-Q4_K_M.gguf", "d784ce9eda1a5a7b51e8f705a9e6310844bf4f173654d115823c775fdea56d43", 6169341984)},
	{ID: "qwen3.5-27b-q4km", Label: "Qwen3.5 27B Q4_K_M", MinGiB: 20, MaxGiB: 47, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-27B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-27B-GGUF", "Qwen_Qwen3.5-27B-Q4_K_M.gguf", "81657841d62f1821c748d0fea6c260b7d3508844fe4e9250253ef81c4e4d9edf", 17984872928)},
	{ID: "qwen3.5-35b-a3b-q4km", Label: "Qwen3.5 35B-A3B Q4_K_M", MinGiB: 48, Source: "https://huggingface.co/bartowski/Qwen_Qwen3.5-35B-A3B-GGUF", Artifact: modelArtifact("bartowski/Qwen_Qwen3.5-35B-A3B-GGUF", "Qwen_Qwen3.5-35B-A3B-Q4_K_M.gguf", "2f2df1e8b2e92b642c1850ea1734b341cc8ca5098c42cc0a8b8c436a8d4751ab", 22285080384)},
}

func (s *Server) initModelInstaller() {
	startup := ""
	if roaming := os.Getenv("APPDATA"); roaming != "" {
		startup = filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	}
	s.modelInstaller = modelinstall.New(s.roots.Data, startup, nil, pinnedLlamaRuntime, pinnedModels, s.installedModelReady)
}

func (s *Server) installedModelReady(_ context.Context, state modelinstall.State) error {
	s.mu.Lock()
	next := *s.cfg
	next.Servers = append([]config.Profile(nil), s.cfg.Servers...)
	profile := config.Profile{ID: state.ProfileID, Label: "local", BaseURL: state.BaseURL, Model: state.Model, ProbeMode: "full", AttachmentHandling: "auto"}
	replaced := false
	for index := range next.Servers {
		if next.Servers[index].ID == profile.ID {
			next.Servers[index] = profile
			replaced = true
		}
	}
	if !replaced {
		next.Servers = append(next.Servers, profile)
	}
	config.ApplyDefaults(&next)
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := next.Save(s.configPath); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cfg = &next
	masked := next.Masked()
	s.mu.Unlock()
	s.bus.Publish(events.New(events.ConfigChanged, "", "", map[string]any{"config": masked}))
	if saved, ok := s.Profile(profile.ID); ok {
		s.startProbe(saved)
	}
	return nil
}

func (s *Server) modelInstall(w http.ResponseWriter, r *http.Request) {
	if s.modelInstaller == nil {
		writeError(w, http.StatusConflict, "local model installation is unavailable", "model_install")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"catalog": s.modelInstaller.Catalog(0, true, true), "state": s.modelInstaller.Snapshot()})
	case http.MethodPost:
		var body struct {
			ModelID string `json:"model_id"`
			Backend string `json:"backend"`
		}
		if !decode(w, r, &body) {
			return
		}
		if err := s.modelInstaller.Start(context.Background(), modelinstall.Request{ModelID: body.ModelID, Backend: body.Backend}); err != nil {
			writeError(w, http.StatusConflict, err.Error(), "model_install")
			return
		}
		writeJSON(w, http.StatusAccepted, s.modelInstaller.Snapshot())
	default:
		method(w)
	}
}

// Marshal compile-time catalog values in one test-friendly form without
// exposing mutable URLs to the browser request body.
func pinnedModelCatalogJSON() []byte { value, _ := json.Marshal(pinnedModels); return value }
