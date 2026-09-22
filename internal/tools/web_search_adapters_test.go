package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebSearchAdapterFixtures(t *testing.T) {
	adapters := map[string]webSearchAdapter{}
	for _, adapter := range defaultWebSearchAdapters() {
		adapters[adapter.Name()] = adapter
	}
	for _, name := range []string{"duckduckgo_html", "duckduckgo_lite", "bing", "brave", "startpage", "mojeek", "wikipedia", "github", "hacker_news", "arxiv", "stackexchange", "pkg_go_dev", "npm"} {
		t.Run(name, func(t *testing.T) {
			ext := ".html"
			if name == "wikipedia" || name == "github" || name == "hacker_news" || name == "stackexchange" || name == "npm" {
				ext = ".json"
			}
			if name == "arxiv" {
				ext = ".xml"
			}
			body, err := os.ReadFile(filepath.Join("testdata", "websearch", name+ext))
			if err != nil {
				t.Fatal(err)
			}
			hits, err := adapters[name].Parse(body, "https://example.test/search", 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 {
				t.Fatalf("hits=%d, want 1", len(hits))
			}
			if hits[0].Title == "" || !strings.HasPrefix(hits[0].URL, "https://") {
				t.Fatalf("bad hit: %+v", hits[0])
			}
			if hits[0].Engine != name || hits[0].Rank != 1 {
				t.Fatalf("identity/rank: %+v", hits[0])
			}
		})
	}
}

func TestWebSearchAdapterRouting(t *testing.T) {
	adapters := map[string]webSearchAdapter{}
	for _, adapter := range defaultWebSearchAdapters() {
		adapters[adapter.Name()] = adapter
	}
	if _, ok := adapters["github"].URL("weather today", webSearchWeb, 5); ok {
		t.Fatal("github routed an unrelated query")
	}
	if _, ok := adapters["github"].URL("github context repository", webSearchWeb, 5); !ok {
		t.Fatal("github did not route a repository query")
	}
	if raw, ok := adapters["bing"].URL("today", webSearchNews, 5); !ok || !strings.Contains(raw, "/news/search") {
		t.Fatalf("bing news route=%q ok=%t", raw, ok)
	}
	if _, ok := adapters["wikipedia"].URL("what is context", webSearchNews, 5); ok {
		t.Fatal("wikipedia routed a news query")
	}
}

func TestWebSearchAdaptersUsePublicHTTPSURLs(t *testing.T) {
	for _, adapter := range defaultWebSearchAdapters() {
		raw, ok := adapter.URL("golang context", webSearchWeb, 5)
		if !ok {
			continue
		}
		if !strings.HasPrefix(raw, "https://") {
			t.Fatalf("%s URL=%q", adapter.Name(), raw)
		}
	}
}
