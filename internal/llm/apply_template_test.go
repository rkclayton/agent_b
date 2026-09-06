package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"harness/internal/config"
)

func TestApplyTemplateErrorIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":{"message":"missing closing quote at column 25065"}}`)
	}))
	defer server.Close()
	profile := &config.Profile{BaseURL: server.URL, RequestTimeoutS: 5}
	_, err := New(profile).ApplyTemplate(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "apply-template HTTP 500") || !strings.Contains(err.Error(), "column 25065") {
		t.Fatalf("error=%v", err)
	}
}
