package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

type fixedSearchAdapter struct {
	name string
	url  string
	hits []webSearchHit
	err  error
}

func (a fixedSearchAdapter) Name() string                                  { return a.name }
func (a fixedSearchAdapter) URL(string, webSearchKind, int) (string, bool) { return a.url, true }
func (a fixedSearchAdapter) Parse([]byte, string, int) ([]webSearchHit, error) {
	hits := append([]webSearchHit(nil), a.hits...)
	for index := range hits {
		hits[index].Engine, hits[index].Rank = a.name, index+1
	}
	return hits, a.err
}

func webSearchTestConfig(names ...string) (config.FetchTool, config.WebSearchTool) {
	return config.FetchTool{TimeoutS: 2, MaxBytes: 1 << 20, MaxRedirects: 3, DefaultLimit: 16 << 10, MaxLimit: 64 << 10, AllowInternalHosts: []string{"127.0.0.1"}}, config.WebSearchTool{Enabled: true, Engines: names, PerEngineTimeoutS: 1, BenchDurationMinutes: 10}
}

func TestWebSearchParallelMergeAndUntrustedEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(80 * time.Millisecond); fmt.Fprint(w, "ok") }))
	defer server.Close()
	fetchCfg, searchCfg := webSearchTestConfig("first", "second")
	shared := "https://Example.com/a/?utm_source=x"
	adapters := []webSearchAdapter{
		fixedSearchAdapter{name: "first", url: server.URL, hits: []webSearchHit{{Title: "Shared", URL: shared, Snippet: "engine summary"}, {Title: "Only", URL: "https://one.example/item"}}},
		fixedSearchAdapter{name: "second", url: server.URL, hits: []webSearchHit{{Title: "Shared alternate", URL: "https://example.com/a", Snippet: "other summary"}}},
	}
	tool := newWebSearch(NewFetch(fetchCfg), searchCfg, adapters, false)
	started := time.Now()
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"query": "context", "limit": 5})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("fan-out was serial: %s", elapsed)
	}
	if !detail.Untrusted || detail.Category != "fetched" || !strings.HasPrefix(detail.Content, fetchWarning) {
		t.Fatalf("classification/content=%+v %q", detail, detail.Content)
	}
	if !strings.Contains(detail.Content, "engines: first, second") || strings.Index(detail.Content, "Shared") > strings.Index(detail.Content, "Only") {
		t.Fatalf("merge/rank result:\n%s", detail.Content)
	}
	if strings.Contains(detail.Content, "<html") {
		t.Fatalf("page content leaked: %s", detail.Content)
	}
}

func TestWebSearchHealthBenchesAndExpires(t *testing.T) {
	fetchCfg, searchCfg := webSearchTestConfig("broken")
	tool := newWebSearch(NewFetch(fetchCfg), searchCfg, nil, false)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	tool.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		tool.recordWebSearchFailure("broken", fmt.Errorf("failure %d", i+1), searchCfg)
	}
	health, ok := tool.healthSnapshot("broken")
	if !ok || health.ConsecutiveFailures != 3 || !health.BenchedUntil.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("health=%+v", health)
	}
	now = now.Add(11 * time.Minute)
	health, _ = tool.healthSnapshot("broken")
	if health.BenchedUntil.After(now) {
		t.Fatalf("bench did not expire: %+v", health)
	}
	tool.recordWebSearchSuccess("broken")
	health, _ = tool.healthSnapshot("broken")
	if health.ConsecutiveFailures != 0 || health.LastSuccess.IsZero() {
		t.Fatalf("success did not reset health: %+v", health)
	}
}

func TestWebSearchPerEngineTimeoutDegrades(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	defer server.Close()
	fetchCfg, searchCfg := webSearchTestConfig("slow")
	tool := newWebSearch(NewFetch(fetchCfg), searchCfg, []webSearchAdapter{fixedSearchAdapter{name: "slow", url: server.URL, hits: []webSearchHit{{Title: "late", URL: "https://example.com"}}}}, false)
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"query": "context"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "no search engine") || !strings.Contains(detail.Content, "engines_unavailable") {
		t.Fatalf("timeout detail=%+v", detail)
	}
}

func TestWebSearchRefusesRedirectToPrivateAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	fetchCfg, searchCfg := webSearchTestConfig("redirect")
	fetch := NewFetch(fetchCfg)
	tool := newWebSearch(fetch, searchCfg, nil, false)
	client := fetch.client(fetchCfg)
	defer client.CloseIdleConnections()
	_, err := tool.searchOne(context.Background(), client, fetchCfg, fixedSearchAdapter{name: "redirect", url: server.URL}, server.URL, 5)
	if err == nil || !strings.Contains(err.Error(), "always refuses") {
		t.Fatalf("private redirect err=%v", err)
	}
}

func TestWebSearchNewsFeedFixture(t *testing.T) {
	feed := webSearchNewsFeed{Outlet: "fixture"}
	body := []byte(`<?xml version="1.0"?><rss><channel><item><title>Today</title><link>https://news.example/today</link><description><![CDATA[<p>Summary only.</p>]]></description></item></channel></rss>`)
	hits, err := feed.Parse(body, "", 5)
	if err != nil || len(hits) != 1 || hits[0].Snippet != "Summary only." || hits[0].Engine != "feed_fixture" {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestWebSearchSchemaAndDescriptionContract(t *testing.T) {
	tool := &WebSearch{}
	if words := len(strings.Fields(tool.Description())); words >= 80 {
		t.Fatalf("description words=%d", words)
	}
	schema := tool.Schema()
	properties := schema["properties"].(map[string]any)
	if len(properties) != 3 || properties["query"] == nil || properties["kind"] == nil || properties["limit"] == nil {
		t.Fatalf("schema=%v", schema)
	}
}

func TestNormalizeWebSearchURLDropsTracking(t *testing.T) {
	left := normalizeWebSearchURL("HTTPS://WWW.Example.COM/a/?utm_source=x&b=2#part")
	right := normalizeWebSearchURL("https://example.com/a?b=2")
	if left != right {
		t.Fatalf("left=%q right=%q", left, right)
	}
}
