package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"

	"github.com/iulita-ai/iulita/internal/web"
)

const maxContentLen = 8000

// Skill fetches and extracts readable content from web pages.
type Skill struct {
	httpClient   *http.Client
	skipURLCheck bool // for testing with httptest localhost servers
}

// New creates a new webfetch skill.
// If httpClient is nil, a SSRF-protected client is used by default.
func New(httpClient *http.Client) *Skill {
	if httpClient == nil {
		httpClient = web.NewSafeHTTPClient(15*time.Second, nil)
	}
	return &Skill{httpClient: httpClient}
}

func (s *Skill) Name() string { return "webfetch" }

func (s *Skill) Description() string {
	return "Fetch and extract readable text content from a web page URL."
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "URL to fetch"
			}
		},
		"required": ["url"]
	}`)
}

func (s *Skill) RequiredCapabilities() []string {
	return []string{"web"}
}

type input struct {
	URL string `json:"url"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	// Bound the entire fetch+parse so slow servers can't block forever.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	if in.URL == "" {
		return "", fmt.Errorf("url is required")
	}

	// Application-level SSRF check — catches private IPs even when a proxy
	// is configured and the dialer-level check can't see the real target.
	if !s.skipURLCheck {
		if err := web.CheckURLSSRF(ctx, in.URL); err != nil {
			return "", err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Iulita/1.0)")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("URL returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	article, err := readability.FromReader(strings.NewReader(string(body)), resp.Request.URL)
	if err != nil {
		// Fallback: return raw text truncated with safety markers.
		text := string(body)
		if len(text) > maxContentLen {
			text = text[:maxContentLen] + "\n...(truncated)"
		}
		return fmt.Sprintf("<<<EXTERNAL_CONTENT url=%q>>>\n%s\n<<<END_EXTERNAL_CONTENT>>>", in.URL, text), nil
	}

	var textBuf strings.Builder
	if err := article.RenderText(&textBuf); err != nil {
		return "", fmt.Errorf("rendering text: %w", err)
	}

	var b strings.Builder
	b.WriteString("<<<EXTERNAL_CONTENT url=")
	fmt.Fprintf(&b, "%q", in.URL)
	b.WriteString(">>>\n")
	if title := article.Title(); title != "" {
		fmt.Fprintf(&b, "Title: %s\n\n", title)
	}
	content := textBuf.String()
	if len(content) > maxContentLen {
		content = content[:maxContentLen] + "\n...(truncated)"
	}
	b.WriteString(content)
	b.WriteString("\n<<<END_EXTERNAL_CONTENT>>>")
	return b.String(), nil
}
