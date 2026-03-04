package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// DDGClient performs web searches via DuckDuckGo HTML (no API key required).
type DDGClient struct {
	httpClient *http.Client
}

// NewDDGClient creates a new DuckDuckGo search client.
func NewDDGClient(httpClient *http.Client) *DDGClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &DDGClient{httpClient: httpClient}
}

// Search implements Searcher via DuckDuckGo HTML scraping.
func (c *DDGClient) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	u := "https://html.duckduckgo.com/html/"

	form := url.Values{}
	form.Set("q", query)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating DDG request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Iulita/1.0)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DDG request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DDG returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // 512KB limit
	if err != nil {
		return nil, fmt.Errorf("reading DDG response: %w", err)
	}

	results := parseDDGHTML(string(body), count)
	return results, nil
}

// parseDDGHTML extracts search results from DuckDuckGo HTML response.
// DDG HTML uses <a class="result__a"> for titles/URLs and
// <a class="result__snippet"> for descriptions.
func parseDDGHTML(body string, maxResults int) []SearchResult {
	tokenizer := html.NewTokenizer(strings.NewReader(body))

	var results []SearchResult
	for {
		if len(results) >= maxResults {
			break
		}

		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return results //nolint:nilerr
		case html.StartTagToken:
			tn, hasAttr := tokenizer.TagName()
			if string(tn) != "a" || !hasAttr {
				continue
			}

			attrs := readAttrs(tokenizer)
			class := attrs["class"]
			href := attrs["href"]

			if class == "result__a" && href != "" {
				title := extractText(tokenizer, "a")
				parsedURL := extractDDGURL(href)
				if parsedURL == "" || title == "" {
					continue
				}
				results = append(results, SearchResult{
					Title: title,
					URL:   parsedURL,
				})
			} else if class == "result__snippet" && len(results) > 0 {
				snippet := extractText(tokenizer, "a")
				results[len(results)-1].Description = strings.TrimSpace(snippet)
			}
		}
	}
	return results
}

// readAttrs reads all attributes from the current token.
func readAttrs(tokenizer *html.Tokenizer) map[string]string {
	attrs := make(map[string]string)
	for {
		key, val, more := tokenizer.TagAttr()
		if len(key) > 0 {
			attrs[string(key)] = string(val)
		}
		if !more {
			break
		}
	}
	return attrs
}

// extractText reads text content until the closing tag.
func extractText(tokenizer *html.Tokenizer, tag string) string {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			b.Write(tokenizer.Text())
		case html.StartTagToken:
			tn, _ := tokenizer.TagName()
			if string(tn) == tag {
				depth++
			}
		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			if string(tn) == tag {
				depth--
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// extractDDGURL extracts the actual URL from DDG's redirect URL.
// DDG wraps URLs like: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com&...
func extractDDGURL(href string) string {
	if strings.HasPrefix(href, "//duckduckgo.com/l/") {
		if u, err := url.Parse("https:" + href); err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	// Direct URL (some results aren't wrapped).
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}
