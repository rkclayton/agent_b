package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"harness/internal/config"
	"harness/internal/session"
)

//go:embed web_search_news.json
var webSearchNewsJSON []byte

// Item 2lq: a benched engine is a decision with a date, not a silence.
//
// rel-1.17.0/W0 probed every benched engine against the capability suite's own
// fixed query and the bench list turned out to be three days stale:
//
//	duckduckgo_html   10 results, no error   -> REVIVED, the reason was untrue
//	duckduckgo_lite   10 results, no error   -> REVIVED, the reason was untrue
//	startpage          0 results, HTTP 200   -> RETIRED, see below
//	mojeek             0 results, HTTP 403   -> RETIRED, blocked at the source
//
// The merge had been running two engines short since 2026-09-23 for a reason
// that had stopped being true. That is the state this item exists to end: every
// entry below carries what was observed and when it was observed.
var initiallyBenchedWebSearchEngines = map[string]string{}

// retiredWebSearchEngines records engines that were removed, so the next reader
// does not wonder where they went or quietly add them back. (b): broken at the
// source means retired and removed, not left as a permanent skipped arm.
var retiredWebSearchEngines = map[string]string{
	"startpage": "2026-09-26: serves an Anubis proof-of-work challenge instead of results (HTTP 200 with a challenge document, no result markup). Not a scraper fix -- passing it means doing the work the challenge demands.",
	"mojeek":    "2026-09-26: HTTP 403 to the fixed query. Blocked at the source.",
}

type webSearchHealth struct {
	ConsecutiveFailures int
	LastError           string
	LastSuccess         time.Time
	BenchedUntil        time.Time
	// Item 2lq (c): why it is benched, in the words of whatever benched it. A
	// bench with no reason is the silence this item exists to end.
	BenchReason string
}

// Item 2lq (c): brave answers HTTP 429 to the capability suite's fixed query, and
// the old code could not tell that apart from a parser that had stopped working:
// three of anything benched the engine for the same fixed interval with
// "HTTP status 429" as the whole explanation.
//
// A 429 is not a broken engine. It is the engine saying come back later, and it
// often says WHEN. So a rate limit is its own outcome: it benches immediately
// rather than on the third try, it benches for as long as the server asked
// (bounded, because Retry-After is attacker-adjacent input), and it records that
// it was rate-limited rather than that it failed.
type webSearchRateLimited struct {
	status int
	after  time.Duration
}

func (e *webSearchRateLimited) Error() string {
	if e.after > 0 {
		return fmt.Sprintf("HTTP status %d (rate limited, waiting %s)", e.status, e.after)
	}
	return fmt.Sprintf("HTTP status %d (rate limited, no Retry-After given)", e.status)
}

// The bench window a rate limit buys. The floor keeps a server that says "0"
// from buying nothing; the ceiling keeps one that says a week from parking an
// engine for a week.
const (
	webSearchRateLimitFloor   = 2 * time.Minute
	webSearchRateLimitCeiling = 60 * time.Minute
)

// rateLimitWindow reads Retry-After, which is either seconds or an HTTP date,
// and clamps whatever it finds. An unreadable or absent value falls back to the
// configured bench duration, so a rate limit always buys a real pause.
func rateLimitWindow(header string, fallback time.Duration) time.Duration {
	window := fallback
	header = strings.TrimSpace(header)
	if header != "" {
		if seconds, err := strconv.Atoi(header); err == nil {
			window = time.Duration(seconds) * time.Second
		} else if when, err := http.ParseTime(header); err == nil {
			window = time.Until(when)
		}
	}
	if window < webSearchRateLimitFloor {
		window = webSearchRateLimitFloor
	}
	if window > webSearchRateLimitCeiling {
		window = webSearchRateLimitCeiling
	}
	return window
}

type webSearchEngineOutcome struct {
	Name string
	Hits []webSearchHit
	Err  error
}

type WebSearch struct {
	mu       sync.RWMutex
	cfg      config.WebSearchTool
	fetch    *Fetch
	adapters []webSearchAdapter
	health   map[string]webSearchHealth
	now      func() time.Time
}

func NewWebSearch(fetch *Fetch, cfg config.WebSearchTool) *WebSearch {
	return newWebSearch(fetch, cfg, defaultWebSearchAdapters(), true)
}

func newWebSearch(fetch *Fetch, cfg config.WebSearchTool, adapters []webSearchAdapter, initialBench bool) *WebSearch {
	tool := &WebSearch{cfg: cfg, fetch: fetch, adapters: append([]webSearchAdapter(nil), adapters...), health: map[string]webSearchHealth{}, now: time.Now}
	if initialBench {
		until := tool.now().Add(time.Duration(cfg.BenchDurationMinutes) * time.Minute)
		for name, reason := range initiallyBenchedWebSearchEngines {
			tool.health[name] = webSearchHealth{ConsecutiveFailures: 3, LastError: reason, BenchedUntil: until, BenchReason: reason}
		}
	}
	return tool
}

func (*WebSearch) Name() string { return "web_search" }
func (*WebSearch) Description() string {
	return "Search the public web or news without keys. Returns ranked URLs and snippets, never page content; use fetch_url on the result you choose."
}
func (*WebSearch) ResultCategory() string { return "fetched" }
func (*WebSearch) ResultUntrusted() bool  { return true }
func (*WebSearch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query"},
			"kind":  map[string]any{"type": "string", "enum": []string{"web", "news"}, "default": "web"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10, "default": webSearchDefaultLimit, "description": "ten unless set lower"},
		},
		"required": []string{"query"},
	}
}

// Item 2if: ten, because the model never set the limit and every call came back
// in blocks of five. One constant, so the tool and the gate that measures it
// cannot drift apart -- the capability table hard-coded 5 and could never have
// shown what shipped.
const webSearchDefaultLimit = 10

func (w *WebSearch) Configure(value config.Config) {
	w.mu.Lock()
	w.cfg = value.Tools.WebSearch
	w.mu.Unlock()
}

func (w *WebSearch) Call(ctx context.Context, s *session.Session, args map[string]any) (string, error) {
	detail := w.CallDetailed(ctx, s, args)
	return detail.Content, detail.Err
}

func (w *WebSearch) CallDetailed(ctx context.Context, s *session.Session, args map[string]any) (detail CallDetail) {
	detail.Category, detail.Untrusted = "fetched", true
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		detail.Err = fmt.Errorf("query is required")
		return detail
	}
	kind := webSearchKind(strings.ToLower(strings.TrimSpace(stringValue(args["kind"], "web"))))
	if kind != webSearchWeb && kind != webSearchNews {
		detail.Err = fmt.Errorf("kind must be web or news")
		return detail
	}
	// Item 2if. The operator: "why is web search always in blocks of 5?" -- the
	// schema said 5 and the model never set it, so every call returned five.
	// "i want it set to 10, the model is calling it multiple times anyway."
	limit := number(args["limit"], webSearchDefaultLimit)
	if limit < 1 || limit > 10 {
		detail.Err = fmt.Errorf("limit must be between 1 and 10")
		return detail
	}

	w.mu.RLock()
	cfg := w.cfg
	w.mu.RUnlock()
	if !cfg.Enabled {
		detail.Err = fmt.Errorf("web_search is disabled in tools.web_search")
		return detail
	}
	fetchCfg := w.fetch.config()
	if s != nil {
		policy := s.Policy().Fetch
		if len(policy.AllowDomains) > 0 {
			fetchCfg.AllowDomains = append([]string(nil), policy.AllowDomains...)
		}
		if len(policy.DenyDomains) > 0 {
			fetchCfg.DenyDomains = append(fetchCfg.DenyDomains, policy.DenyDomains...)
		}
	}
	client := w.fetch.client(fetchCfg)
	defer client.CloseIdleConnections()

	enabled := map[string]bool{}
	for _, name := range cfg.Engines {
		enabled[name] = true
	}
	now := w.now()
	selected := make([]struct {
		adapter webSearchAdapter
		rawURL  string
	}, 0, len(w.adapters))
	benched := []string{}
	for _, adapter := range w.adapters {
		if !enabled[adapter.Name()] {
			continue
		}
		rawURL, applies := adapter.URL(query, kind, limit)
		if !applies {
			continue
		}
		if health, ok := w.healthSnapshot(adapter.Name()); ok && health.BenchedUntil.After(now) {
			// (b): a benched engine is reported WITH its reason, dated. The
			// caller used to be handed a bare name and a timestamp.
			reason := health.BenchReason
			if reason == "" {
				reason = health.LastError
			}
			if reason == "" {
				reason = "no reason recorded"
			}
			benched = append(benched, fmt.Sprintf("%s(until %s: %s)", adapter.Name(), health.BenchedUntil.UTC().Format(time.RFC3339), reason))
			continue
		}
		selected = append(selected, struct {
			adapter webSearchAdapter
			rawURL  string
		}{adapter, rawURL})
	}

	outcomes := make(chan webSearchEngineOutcome, len(selected))
	var group sync.WaitGroup
	for _, item := range selected {
		group.Add(1)
		go func(adapter webSearchAdapter, rawURL string) {
			defer group.Done()
			requestCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.PerEngineTimeoutS)*time.Second)
			defer cancel()
			hits, err := w.searchOne(requestCtx, client, fetchCfg, adapter, rawURL, limit)
			outcomes <- webSearchEngineOutcome{Name: adapter.Name(), Hits: hits, Err: err}
		}(item.adapter, item.rawURL)
	}
	group.Wait()
	close(outcomes)
	answered, unavailable := []string{}, []string{}
	allHits := []webSearchHit{}
	for outcome := range outcomes {
		if outcome.Err != nil || len(outcome.Hits) == 0 {
			if outcome.Err == nil {
				outcome.Err = fmt.Errorf("empty parse")
			}
			w.recordWebSearchFailure(outcome.Name, outcome.Err, cfg)
			unavailable = append(unavailable, outcome.Name+":"+outcome.Err.Error())
			continue
		}
		w.recordWebSearchSuccess(outcome.Name)
		answered = append(answered, outcome.Name)
		allHits = append(allHits, outcome.Hits...)
	}

	if kind == webSearchNews && len(mergeWebSearchHits(allHits, limit)) < limit {
		feeds := loadWebSearchNewsFeeds()
		feedOutcomes := make(chan webSearchEngineOutcome, len(feeds))
		for _, feed := range feeds {
			group.Add(1)
			go func(adapter webSearchAdapter, rawURL string) {
				defer group.Done()
				requestCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.PerEngineTimeoutS)*time.Second)
				defer cancel()
				hits, err := w.searchOne(requestCtx, client, fetchCfg, adapter, rawURL, limit)
				feedOutcomes <- webSearchEngineOutcome{Name: adapter.Name(), Hits: hits, Err: err}
			}(feed, feed.rawURL)
		}
		group.Wait()
		close(feedOutcomes)
		for outcome := range feedOutcomes {
			if outcome.Err != nil || len(outcome.Hits) == 0 {
				if outcome.Err == nil {
					outcome.Err = fmt.Errorf("empty parse")
				}
				unavailable = append(unavailable, outcome.Name+":"+outcome.Err.Error())
				continue
			}
			answered = append(answered, outcome.Name)
			allHits = append(allHits, outcome.Hits...)
		}
	}

	sort.Strings(answered)
	sort.Strings(unavailable)
	sort.Strings(benched)
	merged := mergeWebSearchHits(allHits, limit)
	detail.Metadata = map[string]any{"query": query, "kind": string(kind), "engines_answered": answered, "engines_unavailable": unavailable, "engines_benched": benched, "result_count": len(merged)}
	detail.Content = formatWebSearchResult(query, kind, merged, answered, unavailable, benched)
	if len(merged) == 0 {
		detail.Err = fmt.Errorf("no search engine returned results; %s", strings.Join(append(unavailable, benched...), "; "))
	}
	return detail
}

func (w *WebSearch) searchOne(ctx context.Context, client *http.Client, cfg config.FetchTool, adapter webSearchAdapter, rawURL string, limit int) ([]webSearchHit, error) {
	target, err := parseFetchURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := w.fetch.validateTarget(target, cfg); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,application/atom+xml,application/rss+xml,text/plain;q=0.9,*/*;q=0.8")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer response.Body.Close()
	// (c): a rate limit is a distinct outcome, not one more failure.
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable {
		fallback := time.Duration(w.configuredBenchMinutes()) * time.Minute
		return nil, &webSearchRateLimited{
			status: response.StatusCode,
			after:  rateLimitWindow(response.Header.Get("Retry-After"), fallback),
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, cfg.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > cfg.MaxBytes {
		return nil, fmt.Errorf("response exceeded %d bytes", cfg.MaxBytes)
	}
	finalURL := target.String()
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	hits, err := adapter.Parse(body, finalURL, limit)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return hits, nil
}

func (w *WebSearch) healthSnapshot(name string) (webSearchHealth, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	value, ok := w.health[name]
	return value, ok
}
func (w *WebSearch) recordWebSearchSuccess(name string) {
	w.mu.Lock()
	w.health[name] = webSearchHealth{LastSuccess: w.now()}
	w.mu.Unlock()
}
func (w *WebSearch) configuredBenchMinutes() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cfg.BenchDurationMinutes
}

func (w *WebSearch) recordWebSearchFailure(name string, err error, cfg config.WebSearchTool) {
	w.mu.Lock()
	health := w.health[name]
	health.LastError = err.Error()
	// (c): a rate limit benches on the FIRST one, for as long as the engine
	// asked, and says that is why. It does not count towards the broken-parser
	// tally, because waiting is not breaking.
	var limited *webSearchRateLimited
	if errors.As(err, &limited) {
		health.BenchedUntil = w.now().Add(limited.after)
		health.BenchReason = fmt.Sprintf("rate limited (HTTP %d) on %s", limited.status, w.now().UTC().Format("2006-01-02"))
		w.health[name] = health
		w.mu.Unlock()
		return
	}
	health.ConsecutiveFailures++
	if health.ConsecutiveFailures >= 3 {
		health.BenchedUntil = w.now().Add(time.Duration(cfg.BenchDurationMinutes) * time.Minute)
		health.BenchReason = fmt.Sprintf("%d consecutive failures, last: %s, on %s",
			health.ConsecutiveFailures, err.Error(), w.now().UTC().Format("2006-01-02"))
	}
	w.health[name] = health
	w.mu.Unlock()
}

type mergedWebSearchHit struct {
	Title, URL, Snippet            string
	Engines                        []string
	Agreement, BestRank, RankTotal int
}

func mergeWebSearchHits(hits []webSearchHit, limit int) []mergedWebSearchHit {
	byURL := map[string]*mergedWebSearchHit{}
	for _, hit := range hits {
		key := normalizeWebSearchURL(hit.URL)
		if key == "" {
			continue
		}
		item := byURL[key]
		if item == nil {
			item = &mergedWebSearchHit{Title: hit.Title, URL: hit.URL, Snippet: hit.Snippet, BestRank: hit.Rank}
			byURL[key] = item
		}
		if hit.Rank < item.BestRank {
			item.BestRank = hit.Rank
		}
		item.RankTotal += hit.Rank
		if !containsString(item.Engines, hit.Engine) {
			item.Engines = append(item.Engines, hit.Engine)
		}
		if item.Snippet == "" {
			item.Snippet = hit.Snippet
		}
		if item.Title == "" {
			item.Title = hit.Title
		}
	}
	out := make([]mergedWebSearchHit, 0, len(byURL))
	for _, item := range byURL {
		sort.Strings(item.Engines)
		item.Agreement = len(item.Engines)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agreement != out[j].Agreement {
			return out[i].Agreement > out[j].Agreement
		}
		if out[i].BestRank != out[j].BestRank {
			return out[i].BestRank < out[j].BestRank
		}
		if out[i].RankTotal != out[j].RankTotal {
			return out[i].RankTotal < out[j].RankTotal
		}
		return out[i].URL < out[j].URL
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
func normalizeWebSearchURL(raw string) string {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Hostname() == "" {
		return ""
	}
	target.Scheme = strings.ToLower(target.Scheme)
	host := strings.ToLower(target.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if port := target.Port(); port != "" {
		host += ":" + port
	}
	target.Host = host
	target.Fragment = ""
	query := target.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
			query.Del(key)
		}
	}
	target.RawQuery = query.Encode()
	if target.Path != "/" {
		target.Path = strings.TrimSuffix(target.Path, "/")
	}
	return target.String()
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fallback
}
func formatWebSearchResult(query string, kind webSearchKind, hits []mergedWebSearchHit, answered, unavailable, benched []string) string {
	var out strings.Builder
	out.WriteString(fetchWarning + "\n")
	fmt.Fprintf(&out, "WEB_SEARCH query=%q kind=%s results=%d\n", query, kind, len(hits))
	fmt.Fprintf(&out, "engines_answered: %s\n", strings.Join(answered, ", "))
	if len(unavailable) > 0 {
		fmt.Fprintf(&out, "engines_unavailable: %s\n", strings.Join(unavailable, "; "))
	}
	if len(benched) > 0 {
		fmt.Fprintf(&out, "engines_benched: %s\n", strings.Join(benched, ", "))
	}
	for index, hit := range hits {
		fmt.Fprintf(&out, "\n%d. %s\nurl: %s\nsnippet: %s\nengines: %s\n", index+1, hit.Title, hit.URL, hit.Snippet, strings.Join(hit.Engines, ", "))
	}
	return strings.TrimSpace(out.String())
}

type webSearchNewsFeed struct {
	Outlet  string `json:"name"`
	Address string `json:"url"`
	rawURL  string
}

func loadWebSearchNewsFeeds() []webSearchNewsFeed {
	var feeds []webSearchNewsFeed
	_ = json.Unmarshal(webSearchNewsJSON, &feeds)
	for index := range feeds {
		feeds[index].rawURL = feeds[index].Address
	}
	return feeds
}
func (f webSearchNewsFeed) Name() string { return "feed_" + f.Outlet }
func (f webSearchNewsFeed) URL(_ string, _ webSearchKind, _ int) (string, bool) {
	return f.rawURL, true
}
func (f webSearchNewsFeed) Parse(body []byte, _ string, limit int) ([]webSearchHit, error) {
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	items := []feedItem{}
	switch strings.ToLower(root.XMLName.Local) {
	case "rss":
		var doc rssDocument
		if err := xml.Unmarshal(body, &doc); err != nil {
			return nil, err
		}
		for _, item := range doc.Channel.Items {
			items = append(items, feedItem{Title: item.Title, Link: item.Link, Date: item.Date, Summary: item.Description})
		}
	case "feed":
		var doc atomDocument
		if err := xml.Unmarshal(body, &doc); err != nil {
			return nil, err
		}
		for _, entry := range doc.Entries {
			link := ""
			for _, candidate := range entry.Links {
				if link == "" || candidate.Rel == "alternate" {
					link = candidate.Href
				}
				if candidate.Rel == "alternate" {
					break
				}
			}
			summary := entry.Summary
			if summary == "" {
				summary = entry.Content
			}
			items = append(items, feedItem{Title: entry.Title, Link: link, Summary: summary})
		}
	default:
		return nil, fmt.Errorf("root element must be rss or feed")
	}
	hits := []webSearchHit{}
	for _, item := range items {
		title := normalizeExtractedText(item.Title)
		link := strings.TrimSpace(item.Link)
		if title == "" || link == "" {
			continue
		}
		hits = append(hits, webSearchHit{Title: title, URL: link, Snippet: normalizeFeedSummary(item.Summary), Engine: f.Name(), Rank: len(hits) + 1})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}
