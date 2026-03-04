package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
)

// SearchResult represents a single web search result.
type SearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// BraveClient performs web searches via the Brave Search API.
type BraveClient struct {
	mu         sync.RWMutex
	apiKey     string
	httpClient *http.Client
}

// NewBraveClient creates a new Brave Search API client.
func NewBraveClient(apiKey string, httpClient *http.Client) *BraveClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &BraveClient{apiKey: apiKey, httpClient: httpClient}
}

// UpdateAPIKey updates the API key at runtime (hot-reload).
func (c *BraveClient) UpdateAPIKey(key string) {
	c.mu.Lock()
	c.apiKey = key
	c.mu.Unlock()
}

func (c *BraveClient) getAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

// Search performs a web search and returns up to count results.
func (c *BraveClient) Search(ctx context.Context, query string, count int) ([]SearchResult, error) {
	u, _ := url.Parse("https://api.search.brave.com/res/v1/web/search")
	q := u.Query()
	q.Set("q", query)
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.getAPIKey())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search API returned status %d", resp.StatusCode)
	}

	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	results := make([]SearchResult, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		results = append(results, SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Description: r.Description,
		})
	}
	return results, nil
}
