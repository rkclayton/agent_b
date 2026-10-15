package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"harness/internal/session"
)

type paperHit struct {
	Title, Authors, Year, Venue, DOI, URL, OpenAccessURL, Index string
}
type paperIndex struct {
	name    string
	address func(string, int) string
}

func paperIndexes() []paperIndex {
	query := func(base string, values url.Values) string { return base + "?" + values.Encode() }
	return []paperIndex{
		{"openalex", func(q string, n int) string {
			return query("https://api.openalex.org/works", url.Values{"search": {q}, "per-page": {strconv.Itoa(n)}})
		}},
		{"semantic_scholar", func(q string, n int) string {
			return query("https://api.semanticscholar.org/graph/v1/paper/search", url.Values{"query": {q}, "limit": {strconv.Itoa(n)}, "fields": {"title,authors,year,venue,externalIds,openAccessPdf,url"}})
		}},
		{"arxiv", func(q string, n int) string {
			return query("https://export.arxiv.org/api/query", url.Values{"search_query": {"all:" + q}, "max_results": {strconv.Itoa(n)}})
		}},
		{"pubmed_central", func(q string, n int) string {
			return query("https://www.ebi.ac.uk/europepmc/webservices/rest/search", url.Values{"query": {q}, "pageSize": {strconv.Itoa(n)}, "format": {"json"}})
		}},
	}
}

func (w *WebSearch) searchPapers(ctx context.Context, s *session.Session, query string, limit int) (detail CallDetail) {
	detail.Category, detail.Untrusted = "fetched", true
	cfg := w.fetch.config()
	if s != nil {
		policy := s.Policy().Fetch
		if len(policy.AllowDomains) > 0 {
			cfg.AllowDomains = append([]string(nil), policy.AllowDomains...)
		}
		if len(policy.DenyDomains) > 0 {
			cfg.DenyDomains = append(cfg.DenyDomains, policy.DenyDomains...)
		}
	}
	client := w.fetch.client(cfg)
	defer client.CloseIdleConnections()
	type outcome struct {
		name string
		hits []paperHit
		err  error
	}
	results := make(chan outcome, 4)
	var group sync.WaitGroup
	for _, index := range paperIndexes() {
		group.Add(1)
		go func(index paperIndex) {
			defer group.Done()
			target := index.address(query, limit)
			parsed, err := parseFetchURL(target)
			if err == nil {
				err = w.fetch.validateTarget(parsed, cfg)
			}
			if err != nil {
				results <- outcome{index.name, nil, err}
				return
			}
			req, err := w.fetch.newRequest(ctx, target)
			if err != nil {
				results <- outcome{index.name, nil, err}
				return
			}
			response, err := client.Do(req)
			if err != nil {
				results <- outcome{index.name, nil, err}
				return
			}
			defer response.Body.Close()
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				results <- outcome{index.name, nil, fmt.Errorf("HTTP status %d", response.StatusCode)}
				return
			}
			decoded, err := decodedResponseBody(response)
			if err != nil {
				results <- outcome{index.name, nil, err}
				return
			}
			defer decoded.Close()
			body, err := io.ReadAll(io.LimitReader(decoded, cfg.MaxBytes+1))
			if err == nil && int64(len(body)) > cfg.MaxBytes {
				err = fmt.Errorf("response exceeded %d bytes", cfg.MaxBytes)
			}
			var hits []paperHit
			if err == nil {
				hits, err = parsePaperResults(index.name, body, limit)
			}
			results <- outcome{index.name, hits, err}
		}(index)
	}
	group.Wait()
	close(results)
	answered, unavailable := []string{}, []string{}
	byIndex := map[string][]paperHit{}
	for result := range results {
		if result.err != nil {
			unavailable = append(unavailable, result.name+":"+result.err.Error())
			continue
		}
		answered = append(answered, result.name)
		byIndex[result.name] = result.hits
	}
	sort.Strings(answered)
	sort.Strings(unavailable)
	hits := []paperHit{}
	positions := map[string]int{}
	for rank := 0; len(hits) < limit; rank++ {
		advanced := false
		for _, index := range paperIndexes() {
			list := byIndex[index.name]
			if rank >= len(list) {
				continue
			}
			advanced = true
			candidate := list[rank]
			key := strings.ToLower(strings.TrimSpace(candidate.DOI))
			if key == "" {
				key = strings.ToLower(strings.Join(strings.Fields(candidate.Title), " "))
			}
			if existing, found := positions[key]; found {
				mergePaperHit(&hits[existing], candidate)
				continue
			}
			positions[key] = len(hits)
			hits = append(hits, candidate)
			if len(hits) == limit {
				break
			}
		}
		if !advanced {
			break
		}
	}
	detail.Metadata = map[string]any{"query": query, "kind": "paper", "engines_answered": answered, "engines_unavailable": unavailable, "result_count": len(hits)}
	var out strings.Builder
	out.WriteString(fetchWarning + "\n")
	fmt.Fprintf(&out, "WEB_SEARCH query=%q kind=paper results=%d\nengines_answered: %s\n", query, len(hits), strings.Join(answered, ", "))
	if len(unavailable) > 0 {
		fmt.Fprintf(&out, "engines_unavailable: %s\n", strings.Join(unavailable, "; "))
	}
	for i, hit := range hits {
		fmt.Fprintf(&out, "\n%d. %s\nauthors: %s\nyear: %s\nvenue: %s\ndoi: %s\nurl: %s\nopen_access: %s\nindex: %s\n", i+1, hit.Title, hit.Authors, hit.Year, hit.Venue, hit.DOI, hit.URL, hit.OpenAccessURL, hit.Index)
	}
	detail.Content = strings.TrimSpace(out.String())
	if len(hits) == 0 {
		detail.Err = fmt.Errorf("no scholarly index returned results; %s", strings.Join(unavailable, "; "))
	}
	return detail
}

func mergePaperHit(existing *paperHit, candidate paperHit) {
	if existing.Authors == "" {
		existing.Authors = candidate.Authors
	}
	if existing.Year == "" || existing.Year == "0" {
		existing.Year = candidate.Year
	}
	if existing.Venue == "" {
		existing.Venue = candidate.Venue
	}
	if existing.DOI == "" {
		existing.DOI = candidate.DOI
	}
	if existing.URL == "" {
		existing.URL = candidate.URL
	}
	if existing.OpenAccessURL == "" {
		existing.OpenAccessURL = candidate.OpenAccessURL
	}
	if !strings.Contains(existing.Index, candidate.Index) {
		existing.Index += "," + candidate.Index
	}
}

func parsePaperResults(name string, body []byte, limit int) ([]paperHit, error) {
	var hits []paperHit
	switch name {
	case "openalex":
		var data struct {
			Results []struct {
				Title       string `json:"title"`
				Year        int    `json:"publication_year"`
				DOI         string `json:"doi"`
				Authorships []struct {
					Author struct {
						DisplayName string `json:"display_name"`
					} `json:"author"`
				} `json:"authorships"`
				Primary struct {
					Source struct {
						DisplayName string `json:"display_name"`
					} `json:"source"`
					PDF     string `json:"pdf_url"`
					Landing string `json:"landing_page_url"`
				} `json:"primary_location"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, err
		}
		for _, item := range data.Results {
			authors := []string{}
			for _, a := range item.Authorships {
				authors = append(authors, a.Author.DisplayName)
			}
			hits = append(hits, paperHit{item.Title, strings.Join(authors, ", "), strconv.Itoa(item.Year), item.Primary.Source.DisplayName, strings.TrimPrefix(item.DOI, "https://doi.org/"), item.Primary.Landing, item.Primary.PDF, name})
		}
	case "semantic_scholar":
		var data struct {
			Data []struct {
				Title      string `json:"title"`
				Year       int    `json:"year"`
				Venue, URL string
				Authors    []struct {
					Name string `json:"name"`
				} `json:"authors"`
				External map[string]string `json:"externalIds"`
				OA       *struct {
					URL string `json:"url"`
				} `json:"openAccessPdf"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, err
		}
		for _, item := range data.Data {
			authors := []string{}
			for _, a := range item.Authors {
				authors = append(authors, a.Name)
			}
			oa := ""
			if item.OA != nil {
				oa = item.OA.URL
			}
			hits = append(hits, paperHit{item.Title, strings.Join(authors, ", "), strconv.Itoa(item.Year), item.Venue, item.External["DOI"], item.URL, oa, name})
		}
	case "arxiv":
		var feed struct {
			Entries []struct {
				Title     string `xml:"title"`
				ID        string `xml:"id"`
				DOI       string `xml:"doi"`
				Published string `xml:"published"`
				Authors   []struct {
					Name string `xml:"name"`
				} `xml:"author"`
				Links []struct {
					Title string `xml:"title,attr"`
					Href  string `xml:"href,attr"`
				} `xml:"link"`
			} `xml:"entry"`
		}
		if err := xml.Unmarshal(body, &feed); err != nil {
			return nil, err
		}
		for _, item := range feed.Entries {
			authors := []string{}
			for _, a := range item.Authors {
				authors = append(authors, a.Name)
			}
			pdf := ""
			for _, link := range item.Links {
				if link.Title == "pdf" {
					pdf = link.Href
				}
			}
			year := item.Published
			if len(year) >= 4 {
				year = year[:4]
			}
			hits = append(hits, paperHit{cleanSearchText(item.Title), strings.Join(authors, ", "), year, "arXiv", item.DOI, item.ID, pdf, name})
		}
	case "pubmed_central":
		var raw struct {
			ResultList struct {
				Results []struct {
					Title   string `json:"title"`
					Author  string `json:"authorString"`
					Year    string `json:"pubYear"`
					Journal string `json:"journalTitle"`
					DOI     string `json:"doi"`
					PMCID   string `json:"pmcid"`
				} `json:"result"`
			} `json:"resultList"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		for _, item := range raw.ResultList.Results {
			page := ""
			pdf := ""
			if item.PMCID != "" {
				page = "https://pmc.ncbi.nlm.nih.gov/articles/" + item.PMCID + "/"
				pdf = page + "pdf/"
			}
			hits = append(hits, paperHit{item.Title, item.Author, item.Year, item.Journal, item.DOI, page, pdf, name})
		}
	default:
		return nil, fmt.Errorf("unknown paper index %s", name)
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}
