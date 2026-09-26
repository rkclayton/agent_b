package tools

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	stdhtml "html"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"
)

type webSearchKind string

const (
	webSearchWeb  webSearchKind = "web"
	webSearchNews webSearchKind = "news"
)

type webSearchHit struct {
	Title   string
	URL     string
	Snippet string
	Engine  string
	Rank    int
}

type webSearchAdapter interface {
	Name() string
	URL(query string, kind webSearchKind, limit int) (string, bool)
	Parse(body []byte, requestURL string, limit int) ([]webSearchHit, error)
}

type htmlSearchAdapter struct {
	name           string
	webURL         func(string, int) string
	newsURL        func(string, int) (string, bool)
	containerTag   string
	containerClass string
	anchorClass    string
	snippetClass   string
}

func (a htmlSearchAdapter) Name() string { return a.name }
func (a htmlSearchAdapter) URL(query string, kind webSearchKind, limit int) (string, bool) {
	if kind == webSearchNews && a.newsURL != nil {
		return a.newsURL(query, limit)
	}
	return a.webURL(query, limit), true
}
func (a htmlSearchAdapter) Parse(body []byte, requestURL string, limit int) ([]webSearchHit, error) {
	root, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}
	base, _ := url.Parse(requestURL)
	containers := findHTMLNodes(root, func(node *xhtml.Node) bool {
		return (a.containerTag == "" || node.Data == a.containerTag) && hasHTMLClass(node, a.containerClass)
	})
	hits := make([]webSearchHit, 0, min(limit, len(containers)))
	seen := map[string]bool{}
	for _, container := range containers {
		anchor := firstHTMLNode(container, func(node *xhtml.Node) bool {
			return node.Data == "a" && (a.anchorClass == "" || hasHTMLClass(node, a.anchorClass)) && htmlAttr(node, "href") != ""
		})
		if anchor == nil {
			continue
		}
		raw := htmlAttr(anchor, "href")
		resolved := resolveSearchURL(base, raw)
		if resolved == "" || seen[resolved] {
			continue
		}
		title := htmlNodeText(anchor)
		if title == "" {
			continue
		}
		snippet := ""
		if a.snippetClass != "" {
			if node := firstHTMLNode(container, func(node *xhtml.Node) bool { return hasHTMLClass(node, a.snippetClass) }); node != nil {
				snippet = htmlNodeText(node)
			}
		}
		seen[resolved] = true
		hits = append(hits, webSearchHit{Title: title, URL: resolved, Snippet: snippet, Engine: a.name, Rank: len(hits) + 1})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

type structuredSearchAdapter struct {
	name  string
	match func(string) bool
	url   func(string, int) string
	parse func([]byte, int) ([]webSearchHit, error)
}

func (a structuredSearchAdapter) Name() string { return a.name }
func (a structuredSearchAdapter) URL(query string, kind webSearchKind, limit int) (string, bool) {
	if kind == webSearchNews && a.name != "hacker_news" {
		return "", false
	}
	if a.match != nil && !a.match(query) {
		return "", false
	}
	return a.url(query, limit), true
}
func (a structuredSearchAdapter) Parse(body []byte, _ string, limit int) ([]webSearchHit, error) {
	hits, err := a.parse(body, limit)
	for index := range hits {
		hits[index].Engine, hits[index].Rank = a.name, index+1
	}
	return hits, err
}

func defaultWebSearchAdapters() []webSearchAdapter {
	query := func(base, key, value string) string {
		values := url.Values{}
		values.Set(key, value)
		return base + "?" + values.Encode()
	}
	contains := func(words ...string) func(string) bool {
		return func(value string) bool {
			value = strings.ToLower(value)
			for _, word := range words {
				if strings.Contains(value, word) {
					return true
				}
			}
			return false
		}
	}
	return []webSearchAdapter{
		htmlSearchAdapter{name: "duckduckgo_html", webURL: func(q string, _ int) string { return query("https://html.duckduckgo.com/html/", "q", q) }, containerClass: "result", anchorClass: "result__a", snippetClass: "result__snippet"},
		htmlSearchAdapter{name: "duckduckgo_lite", webURL: func(q string, _ int) string { return query("https://lite.duckduckgo.com/lite/", "q", q) }, anchorClass: "result-link", snippetClass: "result-snippet"},
		htmlSearchAdapter{name: "bing", webURL: func(q string, n int) string {
			values := url.Values{"q": {q}, "count": {fmt.Sprint(n)}}
			return "https://www.bing.com/search?" + values.Encode()
		}, newsURL: func(q string, n int) (string, bool) {
			values := url.Values{"q": {q}, "count": {fmt.Sprint(n)}}
			return "https://www.bing.com/news/search?" + values.Encode(), true
		}, containerTag: "li", containerClass: "b_algo", snippetClass: "b_caption"},
		htmlSearchAdapter{name: "brave", webURL: func(q string, _ int) string {
			values := url.Values{"q": {q}, "source": {"web"}}
			return "https://search.brave.com/search?" + values.Encode()
		}, newsURL: func(q string, _ int) (string, bool) { return query("https://search.brave.com/news", "q", q), true }, containerClass: "snippet", snippetClass: "snippet-description"},
		// Item 2lq: startpage and mojeek were RETIRED on 2026-09-26. startpage
		// serves an Anubis proof-of-work challenge instead of results and mojeek
		// answers 403; both were probed against the suite's own fixed query. See
		// retiredWebSearchEngines in web_search.go for the observations. They are
		// removed rather than left as permanently skipped arms.
		structuredSearchAdapter{name: "wikipedia", match: contains("what is", "who is", "wikipedia", "definition", "history of"), url: func(q string, n int) string {
			values := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {q}, "srlimit": {fmt.Sprint(n)}, "format": {"json"}, "utf8": {"1"}}
			return "https://en.wikipedia.org/w/api.php?" + values.Encode()
		}, parse: parseWikipediaResults},
		structuredSearchAdapter{name: "github", match: contains("github", "repository", "repo", "source code"), url: func(q string, n int) string {
			values := url.Values{"q": {q}, "per_page": {fmt.Sprint(n)}}
			return "https://api.github.com/search/repositories?" + values.Encode()
		}, parse: parseGitHubResults},
		structuredSearchAdapter{name: "hacker_news", match: contains("hacker news", "hn", "startup", "tech news"), url: func(q string, n int) string {
			values := url.Values{"query": {q}, "hitsPerPage": {fmt.Sprint(n)}}
			return "https://hn.algolia.com/api/v1/search?" + values.Encode()
		}, parse: parseHackerNewsResults},
		structuredSearchAdapter{name: "arxiv", match: contains("arxiv", "paper", "research", "study", "preprint"), url: func(q string, n int) string {
			values := url.Values{"search_query": {"all:" + q}, "max_results": {fmt.Sprint(n)}}
			return "https://export.arxiv.org/api/query?" + values.Encode()
		}, parse: parseArxivResults},
		structuredSearchAdapter{name: "stackexchange", match: contains("error", "exception", "how do i", "how to", "programming", "golang", "javascript", "python"), url: func(q string, n int) string {
			values := url.Values{"order": {"desc"}, "sort": {"relevance"}, "q": {q}, "site": {"stackoverflow"}, "pagesize": {fmt.Sprint(n)}, "filter": {"withbody"}}
			return "https://api.stackexchange.com/2.3/search/advanced?" + values.Encode()
		}, parse: parseStackExchangeResults},
		structuredSearchAdapter{name: "pkg_go_dev", match: contains("golang", "go package", "go module", "pkg.go.dev"), url: func(q string, _ int) string { return query("https://pkg.go.dev/search", "q", q) }, parse: parsePkgGoResults},
		structuredSearchAdapter{name: "npm", match: contains("npm", "node", "javascript", "typescript", "package"), url: func(q string, n int) string {
			values := url.Values{"text": {q}, "size": {fmt.Sprint(n)}}
			return "https://registry.npmjs.org/-/v1/search?" + values.Encode()
		}, parse: parseNPMResults},
	}
}

func parseWikipediaResults(body []byte, limit int) ([]webSearchHit, error) {
	var data struct {
		Query struct {
			Search []struct {
				PageID  int    `json:"pageid"`
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range data.Query.Search {
		if item.PageID <= 0 || item.Title == "" {
			continue
		}
		hits = append(hits, webSearchHit{Title: item.Title, URL: fmt.Sprintf("https://en.wikipedia.org/?curid=%d", item.PageID), Snippet: stripHTMLText(item.Snippet)})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func parseGitHubResults(body []byte, limit int) ([]webSearchHit, error) {
	var data struct {
		Items []struct {
			Name, FullName, HTMLURL, Description string `json:"-"`
		} `json:"items"`
	}
	var raw struct {
		Items []struct {
			Name        string `json:"name"`
			FullName    string `json:"full_name"`
			HTMLURL     string `json:"html_url"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range raw.Items {
		title := item.FullName
		if title == "" {
			title = item.Name
		}
		hits = append(hits, webSearchHit{Title: title, URL: item.HTMLURL, Snippet: item.Description})
		if len(hits) >= limit {
			break
		}
	}
	_ = data
	return hits, nil
}

func parseHackerNewsResults(body []byte, limit int) ([]webSearchHit, error) {
	var data struct {
		Hits []struct {
			Title      string `json:"title"`
			StoryTitle string `json:"story_title"`
			URL        string `json:"url"`
			StoryURL   string `json:"story_url"`
			ObjectID   string `json:"objectID"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range data.Hits {
		title, link := item.Title, item.URL
		if title == "" {
			title = item.StoryTitle
		}
		if link == "" {
			link = item.StoryURL
		}
		if link == "" {
			link = "https://news.ycombinator.com/item?id=" + item.ObjectID
		}
		if title != "" {
			hits = append(hits, webSearchHit{Title: title, URL: link})
			if len(hits) >= limit {
				break
			}
		}
	}
	return hits, nil
}

func parseArxivResults(body []byte, limit int) ([]webSearchHit, error) {
	var feed struct {
		Entries []struct {
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			ID      string `xml:"id"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range feed.Entries {
		hits = append(hits, webSearchHit{Title: cleanSearchText(item.Title), URL: strings.TrimSpace(item.ID), Snippet: cleanSearchText(item.Summary)})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func parseStackExchangeResults(body []byte, limit int) ([]webSearchHit, error) {
	var data struct {
		Items []struct {
			Title string `json:"title"`
			Link  string `json:"link"`
			Body  string `json:"body"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range data.Items {
		hits = append(hits, webSearchHit{Title: stdhtml.UnescapeString(item.Title), URL: item.Link, Snippet: stripHTMLText(item.Body)})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func parsePkgGoResults(body []byte, limit int) ([]webSearchHit, error) {
	document, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse("https://pkg.go.dev/search")
	hits := []webSearchHit{}
	for _, container := range findHTMLNodes(document, func(node *xhtml.Node) bool { return hasExactHTMLClass(node, "SearchSnippet") }) {
		anchor := firstHTMLNode(container, func(node *xhtml.Node) bool {
			return node.Data == "a" && htmlAttr(node, "data-test-id") == "snippet-title" && htmlAttr(node, "href") != ""
		})
		if anchor == nil {
			continue
		}
		resolved := resolveSearchURL(base, htmlAttr(anchor, "href"))
		if resolved == "" {
			continue
		}
		snippet := ""
		if node := firstHTMLNode(container, func(node *xhtml.Node) bool { return hasExactHTMLClass(node, "SearchSnippet-synopsis") }); node != nil {
			snippet = htmlNodeText(node)
		}
		hits = append(hits, webSearchHit{Title: htmlNodeText(anchor), URL: resolved, Snippet: snippet})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func parseNPMResults(body []byte, limit int) ([]webSearchHit, error) {
	var data struct {
		Objects []struct {
			Package struct {
				Name, Description string
				Links             struct {
					NPM string `json:"npm"`
				} `json:"links"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	hits := []webSearchHit{}
	for _, item := range data.Objects {
		hits = append(hits, webSearchHit{Title: item.Package.Name, URL: item.Package.Links.NPM, Snippet: item.Package.Description})
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func findHTMLNodes(root *xhtml.Node, match func(*xhtml.Node) bool) []*xhtml.Node {
	var out []*xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && match(node) {
			out = append(out, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return out
}
func firstHTMLNode(root *xhtml.Node, match func(*xhtml.Node) bool) *xhtml.Node {
	if root.Type == xhtml.ElementNode && match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := firstHTMLNode(child, match); found != nil {
			return found
		}
	}
	return nil
}
func htmlAttr(node *xhtml.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}
func hasHTMLClass(node *xhtml.Node, want string) bool {
	if want == "" {
		return true
	}
	for _, class := range strings.Fields(htmlAttr(node, "class")) {
		if class == want || strings.Contains(class, want) {
			return true
		}
	}
	return false
}

func hasExactHTMLClass(node *xhtml.Node, want string) bool {
	for _, class := range strings.Fields(htmlAttr(node, "class")) {
		if class == want {
			return true
		}
	}
	return false
}
func htmlNodeText(node *xhtml.Node) string {
	var values []string
	var walk func(*xhtml.Node)
	walk = func(item *xhtml.Node) {
		if item.Type == xhtml.TextNode {
			values = append(values, item.Data)
		}
		for child := item.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return cleanSearchText(strings.Join(values, " "))
}
func cleanSearchText(value string) string {
	return strings.Join(strings.Fields(stdhtml.UnescapeString(value)), " ")
}
func stripHTMLText(value string) string {
	root, err := xhtml.Parse(strings.NewReader(value))
	if err != nil {
		return cleanSearchText(value)
	}
	return htmlNodeText(root)
}
func resolveSearchURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	if raw == "" || strings.HasPrefix(raw, "javascript:") {
		return ""
	}
	target, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		target = base.ResolveReference(target)
	}
	if strings.Contains(target.Hostname(), "duckduckgo.com") && target.Path == "/l/" {
		if decoded := target.Query().Get("uddg"); decoded != "" {
			target, err = url.Parse(decoded)
			if err != nil {
				return ""
			}
		}
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return ""
	}
	target.Fragment = ""
	return target.String()
}
