package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	azureutls "github.com/Noooste/utls"

	"harness/internal/config"
	"harness/internal/session"
)

func TestFetchExtractsReadableHTMLAndKeepsLinks(t *testing.T) {
	page, err := extractHTML([]byte(`<!doctype html><html><head><title>Example</title><style>hidden</style></head><body><nav>menu</nav><main><h1>Hello</h1><p>Read <a href="/more">more</a>.</p><script>bad()</script></main></body></html>`), mustURL(t, "https://example.com/base"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Example", "Hello", "Read more (https://example.com/more)"} {
		if !strings.Contains(page, want) {
			t.Fatalf("extracted text %q does not contain %q", page, want)
		}
	}
	for _, unwanted := range []string{"hidden", "menu", "bad()"} {
		if strings.Contains(page, unwanted) {
			t.Fatalf("extracted text retained %q: %q", unwanted, page)
		}
	}
}

func TestFetchChrome155WireProfile2v6(t *testing.T) {
	request, err := NewFetch(fetchTestConfig()).newRequest(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := request.UserAgent(), "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"; got != want {
		t.Fatalf("user agent = %q, want %q", got, want)
	}
	headers := orderedChromeHeaders(request.Header)
	wantOrder := []string{"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests", "user-agent", "accept", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-user", "sec-fetch-dest", "accept-encoding", "accept-language", "priority"}
	if len(headers) != len(wantOrder) {
		t.Fatalf("ordered headers = %v", headers)
	}
	for index, name := range wantOrder {
		if headers[index][0] != name {
			t.Fatalf("header %d = %q, want %q", index, headers[index][0], name)
		}
	}
	spec := chrome155Hello()
	var extension, signatures bool
	for _, item := range spec.Extensions {
		switch value := item.(type) {
		case *azureutls.GenericExtension:
			extension = extension || value.Id == 0xca34 && len(value.Data) == len(chrome155ExtensionData)
		case *azureutls.SignatureAlgorithmsExtension:
			signatures = len(value.SupportedSignatureAlgorithms) >= 4 && value.SupportedSignatureAlgorithms[1] == azureutls.SignatureScheme(0x0904) && value.SupportedSignatureAlgorithms[3] == azureutls.SignatureScheme(0x0906)
		}
	}
	if !extension || !signatures {
		t.Fatalf("Chrome 155 hello extension=%t signatures=%t", extension, signatures)
	}
}

func TestFetchFeedFixturesHaveExactDeterministicOutput(t *testing.T) {
	for _, name := range []string{"rss-cdata", "atom"} {
		data := fetchFixture(t, name+".xml")
		want := fetchExpected(t, name+".expected.txt")
		got, dropped, err := extractFeed(data)
		if err != nil {
			t.Fatal(err)
		}
		actual := fmt.Sprintf("dropped: %d\n%s", dropped, got)
		if actual != want {
			t.Fatalf("%s output:\n%s\nwant:\n%s", name, actual, want)
		}
	}
}

func TestFetchMalformedFeedReportsExactParsePosition(t *testing.T) {
	_, _, err := extractFeed(fetchFixture(t, "malformed.xml"))
	want := fetchExpected(t, "malformed.expected.txt")
	if err == nil || err.Error() != want {
		t.Fatalf("error=%q want=%q", err, want)
	}
}

func TestFetchArticleFixturesHaveExactDeterministicOutput(t *testing.T) {
	base := mustURL(t, "https://example.test/base")
	for _, name := range []string{"news", "docs", "main", "none"} {
		got, dropped, fallback, err := extractArticle(fetchFixture(t, name+".html"), base)
		if err != nil {
			t.Fatal(err)
		}
		actual := fmt.Sprintf("dropped: %d\nfallback: %t\n%s", dropped, fallback, got)
		want := fetchExpected(t, name+".expected.txt")
		if actual != want {
			t.Fatalf("%s output:\n%s\nwant:\n%s", name, actual, want)
		}
	}
}

func TestFetchModesUseSameWindowEnvelopeAndReportDroppedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feed" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(fetchFixture(t, "rss-cdata.xml"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(fetchFixture(t, "main.html"))
	}))
	defer server.Close()
	for _, test := range []struct {
		path, mode, want string
		dropped          int
	}{
		{"/feed", "feed", "# Alpha & Beta", 1},
		{"/article", "article", "Main fixture", 1},
	} {
		detail := NewFetch(fetchTestConfig()).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + test.path, "mode": test.mode, "limit": 64 << 10})
		if detail.Err != nil || !strings.Contains(detail.Content, "extraction_mode: "+test.mode) || !strings.Contains(detail.Content, "> "+test.want) || detail.Metadata["dropped"] != test.dropped {
			t.Fatalf("%s detail=%+v", test.mode, detail)
		}
	}
}

func TestFetchArticleNoMainFallsBackToDefaultWindowExplicitly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(fetchFixture(t, "none.html"))
	}))
	defer server.Close()
	detail := NewFetch(fetchTestConfig()).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL, "mode": "article"})
	if detail.Err != nil || detail.Metadata["extraction_fallback"] != true || !strings.Contains(detail.Content, "no identifiable main content; returned the default byte window") || !strings.Contains(detail.Content, "> Loose heading") {
		t.Fatalf("fallback detail=%+v", detail)
	}
}

func TestFetchPDFWindowsAndNamesMissingTextLayer2v6(t *testing.T) {
	walk, err := os.ReadFile(filepath.Join("..", "attachment", "testdata", "walk-doc.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	blank := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 144]/Contents 4 0 R>>endobj\n4 0 obj<</Length 11>>stream\n0 0 m 1 1 l\nendstream endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		if r.URL.Path == "/blank" {
			_, _ = w.Write(blank)
		} else {
			_, _ = w.Write(walk)
		}
	}))
	defer server.Close()
	tool := NewFetch(fetchTestConfig())
	read := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/paper", "limit": 24})
	if read.Err != nil || !strings.Contains(read.Content, "## Page 1") || !strings.Contains(read.Content, "more: true") || !strings.Contains(read.Content, "next_offset:") {
		t.Fatalf("PDF window=%+v", read)
	}
	empty := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/blank"})
	if empty.Err != nil || !strings.Contains(empty.Content, "PDF has no text layer") {
		t.Fatalf("blank PDF=%+v", empty)
	}
}

func TestFetchFiveURLsRunsInParallelAndKeepsFiveEnvelopes2v6(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, r.URL.Path)
	}))
	defer server.Close()
	urls := []any{}
	for index := 0; index < 5; index++ {
		urls = append(urls, fmt.Sprintf("%s/%d", server.URL, index))
	}
	started := time.Now()
	detail := NewFetch(fetchTestConfig()).CallDetailed(context.Background(), &session.Session{}, map[string]any{"urls": urls})
	if detail.Err != nil || time.Since(started) > 200*time.Millisecond || strings.Count(detail.Content, "[BEGIN UNTRUSTED FETCHED CONTENT]") != 5 {
		t.Fatalf("parallel fetch elapsed=%s detail=%+v", time.Since(started), detail)
	}
}

func TestFetchOffersAndUsesBrowserButStopsAtPaywall2v6(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if r.URL.Path == "/paywall" {
			fmt.Fprint(w, "Subscribe to continue")
		} else {
			fmt.Fprint(w, "Forbidden")
		}
	}))
	defer server.Close()
	tool := NewFetch(fetchTestConfig())
	tool.browser = func(context.Context, string) (string, error) { return "Rendered article words", nil }
	refused := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/refused"})
	if refused.Err == nil || !strings.Contains(refused.Content, "via: browser") {
		t.Fatalf("offer=%+v", refused)
	}
	rendered := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/refused", "via": "browser"})
	if rendered.Err != nil || !strings.Contains(rendered.Content, "Rendered article words") {
		t.Fatalf("browser=%+v", rendered)
	}
	paywall := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/paywall"})
	if paywall.Err != nil || !strings.Contains(paywall.Content, "paywall: Subscribe to continue") || strings.Contains(paywall.Content, "via: browser") {
		t.Fatalf("paywall=%+v", paywall)
	}
}

func TestFetchPaywallNamesOpenAccessAndCachesItPerRun2vb(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		switch r.URL.Path {
		case "/body":
			fmt.Fprint(w, `<meta name="citation_doi" content="10.1111/body-doi">Subscribe to continue`)
		case "/none":
			fmt.Fprint(w, `<meta name="dc.identifier" content="doi:10.1111/no-copy">Subscribe to continue`)
		default:
			fmt.Fprint(w, "Forbidden")
		}
	}))
	defer server.Close()
	tool := NewFetch(fetchTestConfig())
	tool.openAccess = func(_ context.Context, doi string) (string, error) {
		calls.Add(1)
		pdf := ""
		if doi == "10.1111/body-doi" {
			pdf = "https://example.org/free.pdf"
		}
		recorded := fmt.Sprintf(`{"results":[{"title":"Recorded paper","doi":"https://doi.org/%s","primary_location":{"pdf_url":"%s","source":{"display_name":"Recorded index"}}}]}`, doi, pdf)
		hits, err := parsePaperResults("openalex", []byte(recorded), 1)
		if err != nil || len(hits) == 0 {
			return "", err
		}
		return hits[0].OpenAccessURL, nil
	}
	run := &session.Session{ID: "paper", Run: session.RunState{RunID: "one"}}
	for range 2 {
		detail := tool.CallDetailed(context.Background(), run, map[string]any{"url": server.URL + "/body"})
		if !strings.Contains(detail.Content, "paywall:") || !strings.Contains(detail.Content, "open access: https://example.org/free.pdf") {
			t.Fatalf("body=%+v", detail)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same-run index calls=%d", calls.Load())
	}
	none := tool.CallDetailed(context.Background(), run, map[string]any{"url": server.URL + "/none"})
	if !strings.Contains(none.Content, "open access: none found") {
		t.Fatalf("none=%+v", none)
	}
	plain := tool.CallDetailed(context.Background(), run, map[string]any{"url": server.URL + "/plain"})
	if strings.Contains(plain.Content, "open access:") || calls.Load() != 2 {
		t.Fatalf("plain=%+v calls=%d", plain, calls.Load())
	}
	fromURL := tool.CallDetailed(context.Background(), run, map[string]any{"url": server.URL + "/doi/10.1111/url-doi"})
	if !strings.Contains(fromURL.Content, "open access: none found") || calls.Load() != 3 {
		t.Fatalf("url=%+v calls=%d", fromURL, calls.Load())
	}
}

func TestFetchOpenAccessLookupKeepsTheFetchDeadline2vb(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	cfg := fetchTestConfig()
	cfg.TimeoutS = 1
	tool := NewFetch(cfg)
	tool.openAccess = func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	started := time.Now()
	detail := tool.CallDetailed(context.Background(), &session.Session{ID: "paper", Run: session.RunState{RunID: "timeout"}}, map[string]any{"url": server.URL + "/doi/10.1111/slow"})
	if !strings.Contains(detail.Content, "HTTP status 403") || !strings.Contains(detail.Content, "open access: no answer") || time.Since(started) > 2*time.Second {
		t.Fatalf("elapsed=%s detail=%+v", time.Since(started), detail)
	}
}

func fetchFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fetch", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fetchExpected(t *testing.T, name string) string {
	return strings.TrimSuffix(strings.ReplaceAll(string(fetchFixture(t, name)), "\r\n", "\n"), "\n")
}

func TestFetchSingleLineByteWindowsDoNotRepeat(t *testing.T) {
	var source strings.Builder
	for index := 0; index < 120; index++ {
		fmt.Fprintf(&source, "item-%04d|", index)
	}
	source.WriteString("🙂-tail")
	payload := source.String()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, payload)
	}))
	defer server.Close()

	offset := 1
	seen := map[string]bool{}
	var rebuilt strings.Builder
	for {
		detail := NewFetch(fetchTestConfig()).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL, "offset": offset, "limit": 73})
		if detail.Err != nil {
			t.Fatal(detail.Err)
		}
		window := envelopePayload(t, detail.Content)
		if seen[window] {
			t.Fatalf("fetch_url repeated window %q at offset %d", window, offset)
		}
		seen[window] = true
		rebuilt.WriteString(window)
		if detail.Metadata["window_offset"] != offset || detail.Metadata["window_bytes"] != len([]byte(window)) || detail.Metadata["total_bytes"] != len([]byte(payload)) {
			t.Fatalf("window metadata=%#v content=%q", detail.Metadata, window)
		}
		more, _ := detail.Metadata["more"].(bool)
		if !more {
			if _, exists := detail.Metadata["next_offset"]; exists {
				t.Fatalf("completed response retained cursor: %#v", detail.Metadata)
			}
			break
		}
		next, ok := detail.Metadata["next_offset"].(int)
		if !ok || next <= offset {
			t.Fatalf("invalid next cursor in %#v", detail.Metadata)
		}
		offset = next
	}
	if rebuilt.String() != payload {
		t.Fatalf("rebuilt payload=%q want=%q", rebuilt.String(), payload)
	}
}

func TestFetchRedirectsAndLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		fmt.Fprint(w, "arrived")
	}))
	defer server.Close()

	cfg := fetchTestConfig()
	detail := NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/start"})
	if detail.Err != nil || !strings.Contains(detail.Content, "> arrived") {
		t.Fatalf("redirect fetch: content=%q err=%v", detail.Content, detail.Err)
	}
	cfg.MaxRedirects = 0
	detail = NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL + "/start"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "redirect limit") {
		t.Fatalf("redirect limit err=%v", detail.Err)
	}
}

func TestFetchResponseSizeLimitAndEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "123456789")
	}))
	defer server.Close()
	cfg := fetchTestConfig()
	cfg.MaxBytes = 5
	detail := NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL})
	if detail.Err != nil {
		t.Fatal(detail.Err)
	}
	for _, want := range []string{"[BEGIN UNTRUSTED FETCHED CONTENT]", fetchWarning, "source_bytes: 5", "source_truncated: true", "window_offset: 1", "window_bytes: 5", "total_bytes: 5", "more: false", "> 12345", "[END UNTRUSTED FETCHED CONTENT]"} {
		if !strings.Contains(detail.Content, want) {
			t.Fatalf("envelope missing %q: %q", want, detail.Content)
		}
	}
	if detail.Category != "fetched" || !detail.Untrusted || detail.Metadata["source_truncated"] != true {
		t.Fatalf("detail classification: %+v", detail)
	}
}

func TestFetchTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	defer server.Close()
	cfg := fetchTestConfig()
	cfg.TimeoutS = 1
	detail := NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "request failed") {
		t.Fatalf("timeout err=%v", detail.Err)
	}
}

func TestFetchBrowserTimeoutReturnsAnEnvelope2va(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("SKIPPED: browser fallback uses the installed Windows browser; Ubuntu has no equivalent product path.")
	}
	cfg := fetchTestConfig()
	cfg.TimeoutS = 1
	tool := NewFetch(cfg)
	tool.browser = func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	started := time.Now()
	detail := tool.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": "https://example.com/article", "via": "browser"})
	if detail.Err != nil || !detail.Untrusted || !strings.Contains(detail.Content, "browser: no answer in 1 s") {
		t.Fatalf("detail=%+v", detail)
	}
	if elapsed := time.Since(started); elapsed < time.Second || elapsed > 2*time.Second {
		t.Fatalf("browser bound elapsed=%s", elapsed)
	}
}

func TestFetchNon2xxIsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "upstream unavailable")
	}))
	defer server.Close()
	registry := New(NewFetch(fetchTestConfig()))
	item := &session.Session{ToolsEnabled: map[string]bool{"fetch_url": true}}
	outcome := registry.CallDetailed(context.Background(), item, "fetch_url", map[string]any{"url": server.URL})
	if outcome.OK || !strings.Contains(outcome.Content, "HTTP status 502") || outcome.Metadata["status"] != http.StatusBadGateway {
		t.Fatalf("non-2xx outcome = %#v", outcome)
	}
}

func TestFetchRefusesBinaryMIME(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0, 1, 2})
	}))
	defer server.Close()
	detail := NewFetch(fetchTestConfig()).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": server.URL})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "unsupported content type") {
		t.Fatalf("binary MIME err=%v", detail.Err)
	}
}

func TestFetchDomainAllowList(t *testing.T) {
	cfg := fetchTestConfig()
	cfg.AllowDomains = []string{"example.com"}
	detail := NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": "http://not-example.invalid/"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "allow_domains") {
		t.Fatalf("allow-list err=%v", detail.Err)
	}
	if !domainAllowed("docs.example.com", cfg.AllowDomains) {
		t.Fatal("subdomain of allowed domain was refused")
	}
}

func TestFetchDeniedHostReturnsPolicyWithoutLocationRule(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "must not be reached")
	}))
	defer server.Close()
	cfg := fetchTestConfig()
	cfg.AllowInternalHosts = nil
	cfg.DenyDomains = []string{"127.0.0.1"}
	item := &session.Session{ToolsEnabled: map[string]bool{"fetch_url": true}}
	outcome := New(NewFetch(cfg)).CallDetailed(context.Background(), item, "fetch_url", map[string]any{"url": server.URL})
	if outcome.OK || !strings.Contains(outcome.Content, "domain 127.0.0.1 is in tools.fetch.deny_domains") || strings.Contains(strings.ToLower(outcome.Content), "location") || strings.Contains(strings.ToLower(outcome.Content), "identity") || requests.Load() != 0 {
		t.Fatalf("outcome=%+v requests=%d", outcome, requests.Load())
	}

	cfg.AllowDomains = []string{"ipinfo.io"}
	cfg.DenyDomains = []string{"ipinfo.io"}
	if err := validateFetchTarget(mustURL(t, "https://ipinfo.io/json"), cfg); err != nil {
		t.Fatalf("explicit allow_domains did not supersede deny_domains: %v", err)
	}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "stand-in location") })
	standIn := server.Listener.Addr().String()
	cfg = fetchTestConfig()
	cfg.DenyDomains = nil
	fetch := NewFetch(cfg)
	fetch.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, standIn)
	}
	port := server.Listener.Addr().(*net.TCPAddr).Port
	detail := fetch.CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": fmt.Sprintf("http://ipinfo.io:%d/json", port)})
	if detail.Err != nil || !strings.Contains(detail.Content, "stand-in location") {
		t.Fatalf("retired host fetch detail=%+v", detail)
	}
}

func TestFetchRegistryClassifiesRefusalsAsUntrustedFetchedResults(t *testing.T) {
	cfg := fetchTestConfig()
	cfg.AllowInternalHosts = nil
	registry := New(NewFetch(cfg))
	item := &session.Session{ToolsEnabled: map[string]bool{"fetch_url": true}}
	outcome := registry.CallDetailed(context.Background(), item, "fetch_url", map[string]any{"url": "http://127.0.0.1/"})
	if outcome.OK || outcome.Category != "fetched" || !outcome.Untrusted {
		t.Fatalf("outcome classification = %+v", outcome)
	}
}

func TestFetchSSRFGuardAndSpecificInternalException(t *testing.T) {
	cfg := fetchTestConfig()
	cfg.AllowInternalHosts = nil
	detail := NewFetch(cfg).CallDetailed(context.Background(), &session.Session{}, map[string]any{"url": "http://127.0.0.1/"})
	if detail.Err == nil || !strings.Contains(detail.Err.Error(), "SSRF guard") {
		t.Fatalf("SSRF err=%v", detail.Err)
	}
	if err := validateFetchTarget(mustURL(t, "http://127.0.0.1/"), fetchTestConfig()); err != nil {
		t.Fatalf("specific internal exception refused: %v", err)
	}
	for _, raw := range []string{strings.Join([]string{"10", "0", "0", "1"}, "."), strings.Join([]string{"172", "16", "0", "1"}, "."), "192.168.1.10", "169.254.1.1", "100.64.0.10", "::1", "fd00::1", "fe80::1"} {
		if !blockedFetchIP(net.ParseIP(raw)) {
			t.Errorf("private address %s was not blocked", raw)
		}
	}
}

func TestFetchLANPolicyKeepsPermanentRefusals(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	cfg.Shell.AllowLocalNetwork = true
	cfg.Shell.ConfirmedLocalSubnets = []string{"192.168.1.0/24"}
	fetch := NewFetch(cfg.Tools.Fetch)
	fetch.Configure(cfg)
	prefixes := fetch.localNetworkPrefixes()
	if err := validateFetchTarget(mustURL(t, "http://192.168.1.10/"), cfg.Tools.Fetch, prefixes); err != nil {
		t.Fatalf("confirmed LAN refused: %v", err)
	}
	for _, raw := range []string{"http://127.0.0.1:8790/", "http://169.254.1.1/", "http://169.254.169.254/"} {
		var err error
		if strings.Contains(raw, ":8790") {
			cfg.Listen = "127.0.0.1:8790"
			fetch.Configure(cfg)
			err = fetch.validateTarget(mustURL(t, raw), cfg.Tools.Fetch)
		} else {
			err = validateFetchTarget(mustURL(t, raw), cfg.Tools.Fetch, prefixes)
		}
		if err == nil {
			t.Fatalf("permanent refusal accepted: %s", raw)
		}
	}
	cfg.Tools.Fetch.AllowInternalHosts = []string{"169.254.1.1", "169.254.169.254"}
	for _, raw := range []string{"http://169.254.1.1/", "http://169.254.169.254/"} {
		if err := validateFetchTarget(mustURL(t, raw), cfg.Tools.Fetch, prefixes); err == nil || !strings.Contains(err.Error(), "always refuses") {
			t.Fatalf("internal-host exception bypassed permanent refusal: %s err=%v", raw, err)
		}
	}
}

func fetchTestConfig() config.FetchTool {
	return config.FetchTool{TimeoutS: 5, MaxBytes: 1 << 20, MaxRedirects: 3, DefaultLimit: 16 << 10, MaxLimit: 64 << 10, AllowDomains: []string{}, AllowInternalHosts: []string{"127.0.0.1"}}
}

func envelopePayload(t *testing.T, envelope string) string {
	t.Helper()
	for _, line := range strings.Split(envelope, "\n") {
		if strings.HasPrefix(line, "> ") {
			return strings.TrimPrefix(line, "> ")
		}
	}
	t.Fatalf("envelope has no content line: %q", envelope)
	return ""
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// v0.70.1 (2fr cold review): x/net v0.50 refuses HTML nested more than 512
// elements deep. The page's text still comes back, tags removed, with the
// reason, rather than nothing.
func TestAPageNestedTooDeepStillReturnsItsText(t *testing.T) {
	page := "<html><body><script>ignored()</script>" + strings.Repeat("<div>", 600) + "Deep text &amp; more" + strings.Repeat("</div>", 600) + "</body></html>"
	for name, extract := range map[string]func() (string, error){
		"page":    func() (string, error) { return extractHTML([]byte(page), nil) },
		"article": func() (string, error) { text, _, _, err := extractArticle([]byte(page), nil); return text, err },
	} {
		text, err := extract()
		if err != nil || !strings.Contains(text, "Deep text & more") || strings.Contains(text, "ignored") || !strings.Contains(text, "too deeply to parse") {
			t.Fatalf("%s: %q %v", name, text, err)
		}
	}
}
