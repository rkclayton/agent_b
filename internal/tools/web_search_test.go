package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
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

// Item 2lq (c): brave answers 429, and the old code spent three round trips
// finding that out before benching for a duration nobody asked for. A rate limit
// benches on the first one, for as long as the server asked, and says so.
func TestARateLimitBenchesOnTheFirstOne2lq(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	fetchCfg, searchCfg := webSearchTestConfig("limited")
	adapter := fixedSearchAdapter{name: "limited", url: server.URL, hits: []webSearchHit{{Title: "never parsed", URL: "https://example.com"}}}
	tool := newWebSearch(NewFetch(fetchCfg), searchCfg, []webSearchAdapter{adapter}, false)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tool.now = func() time.Time { return now }

	client := tool.fetch.client(fetchCfg)
	defer client.CloseIdleConnections()
	_, err := tool.searchOne(context.Background(), client, fetchCfg, adapter, server.URL, 10)
	var limited *webSearchRateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("a 429 must be its own outcome, got %v", err)
	}
	if limited.after != 5*time.Minute {
		t.Fatalf("Retry-After: 300 should buy 5m, got %s", limited.after)
	}

	tool.recordWebSearchFailure("limited", err, searchCfg)
	health, ok := tool.healthSnapshot("limited")
	if !ok || !health.BenchedUntil.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("the first rate limit must bench for what the server asked: %+v", health)
	}
	// Waiting is not breaking: a rate limit does not walk the engine towards the
	// broken-parser threshold.
	if health.ConsecutiveFailures != 0 {
		t.Fatalf("a rate limit counted as a failure: %+v", health)
	}
	if !strings.Contains(health.BenchReason, "rate limited") || !strings.Contains(health.BenchReason, "2026-09-26") {
		t.Fatalf("the bench reason must name the cause and the date: %q", health.BenchReason)
	}
	// And the caller is told, in the report, with the reason and the date.
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"query": "context"})
	if !strings.Contains(detail.Content, "rate limited") || !strings.Contains(detail.Content, "2026-09-26") {
		t.Fatalf("the benched line dropped the reason:\n%s", detail.Content)
	}

	// The negative control: an ordinary failure still takes three, and is still
	// described as a failure. Without this, the change would bench every engine on
	// its first hiccup.
	plain := newWebSearch(NewFetch(fetchCfg), searchCfg, nil, false)
	plain.now = func() time.Time { return now }
	plain.recordWebSearchFailure("ordinary", fmt.Errorf("parse: no results"), searchCfg)
	if first, _ := plain.healthSnapshot("ordinary"); !first.BenchedUntil.IsZero() || first.ConsecutiveFailures != 1 {
		t.Fatalf("one ordinary failure must not bench: %+v", first)
	}
	plain.recordWebSearchFailure("ordinary", fmt.Errorf("parse: no results"), searchCfg)
	plain.recordWebSearchFailure("ordinary", fmt.Errorf("parse: no results"), searchCfg)
	third, _ := plain.healthSnapshot("ordinary")
	if !third.BenchedUntil.Equal(now.Add(10*time.Minute)) || !strings.Contains(third.BenchReason, "3 consecutive failures") {
		t.Fatalf("the third ordinary failure must bench for the configured duration, with a reason: %+v", third)
	}
}

// Retry-After is a header from a stranger. Whatever it says, the pause it buys is
// bounded at both ends.
func TestRetryAfterIsClamped2lq(t *testing.T) {
	fallback := 10 * time.Minute
	for _, probe := range []struct {
		header string
		want   time.Duration
	}{
		{"", fallback},
		{"0", webSearchRateLimitFloor},
		{"1", webSearchRateLimitFloor},
		{"600", 10 * time.Minute},
		{"604800", webSearchRateLimitCeiling},
		{"-9", webSearchRateLimitFloor},
		{"not a number", fallback},
		{"Wed, 21 Oct 2015 07:28:00 GMT", webSearchRateLimitFloor}, // a date in the past
	} {
		if got := rateLimitWindow(probe.header, fallback); got != probe.want {
			t.Errorf("Retry-After %q gave %s, want %s", probe.header, got, probe.want)
		}
	}
}

// Item 2lq (e): the merge runs with two fewer engines than it did before this
// item retired startpage and mojeek. Ranking is by agreement first, so removing
// an engine changes agreement counts — and the thing to prove is that it changes
// them HONESTLY: a hit only the departed engine returned leaves, the survivors
// keep their relative order, and nothing inherits an agreement it did not earn.
func TestRankingSurvivesOneFewerEngine2lq(t *testing.T) {
	const (
		all   = "https://example.com/all"
		two   = "https://example.com/two"
		alone = "https://example.com/only-from-the-third"
	)
	hit := func(engine, url string, rank int) webSearchHit {
		return webSearchHit{Engine: engine, URL: url, Title: url, Rank: rank}
	}
	three := []webSearchHit{
		hit("first", all, 1), hit("first", two, 2),
		hit("second", all, 1), hit("second", two, 3),
		hit("third", all, 2), hit("third", alone, 1),
	}

	before := mergeWebSearchHits(three, 10)
	if len(before) != 3 || before[0].URL != all || before[0].Agreement != 3 {
		t.Fatalf("three engines: %+v", before)
	}
	if before[1].URL != two || before[1].Agreement != 2 {
		t.Fatalf("three engines, second place: %+v", before)
	}

	// Now the third engine is gone, as startpage and mojeek are gone.
	withoutThird := []webSearchHit{}
	for _, item := range three {
		if item.Engine != "third" {
			withoutThird = append(withoutThird, item)
		}
	}
	after := mergeWebSearchHits(withoutThird, 10)
	if len(after) != 2 {
		t.Fatalf("the hit only the departed engine returned must leave: %+v", after)
	}
	for index, url := range []string{all, two} {
		if after[index].URL != url {
			t.Fatalf("the survivors changed order: position %d is %s, want %s", index, after[index].URL, url)
		}
	}
	// Agreement drops by exactly the engines that left, and by no more.
	if after[0].Agreement != 2 || after[1].Agreement != 2 {
		t.Fatalf("agreement was not recomputed honestly: %+v", after)
	}
	for _, item := range after {
		if len(item.Engines) != item.Agreement {
			t.Fatalf("%s claims agreement %d from engines %v", item.URL, item.Agreement, item.Engines)
		}
		if containsString(item.Engines, "third") {
			t.Fatalf("%s still credits a departed engine: %v", item.URL, item.Engines)
		}
	}
	// And a one-engine merge still ranks, by that engine's own order.
	single := mergeWebSearchHits([]webSearchHit{hit("first", two, 2), hit("first", all, 1)}, 10)
	if len(single) != 2 || single[0].URL != all || single[0].Agreement != 1 {
		t.Fatalf("one engine: %+v", single)
	}
}

// The tool ships the engines it has adapters for, and nothing it retired. And a
// bench or a retirement carries a date and a reason, or it is the silence this
// item exists to end.
func TestRetiredEnginesHaveNoAdapter2lq(t *testing.T) {
	for _, adapter := range defaultWebSearchAdapters() {
		if reason, retired := retiredWebSearchEngines[adapter.Name()]; retired {
			t.Errorf("%s is recorded as retired (%s) but still has an adapter", adapter.Name(), reason)
		}
	}
	dated := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}: \S`)
	for name, reason := range retiredWebSearchEngines {
		if !dated.MatchString(reason) {
			t.Errorf("%s: a retirement must read \"YYYY-MM-DD: why\", got %q", name, reason)
		}
	}
	anyDate := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	for name, reason := range initiallyBenchedWebSearchEngines {
		if !anyDate.MatchString(reason) {
			t.Errorf("%s: a bench must carry the date it was observed, got %q", name, reason)
		}
	}
}
