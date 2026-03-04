package pdfreader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type pdfInput struct {
	URL string `json:"url"` // URL to fetch PDF from
}

// Skill fetches PDF documents from URLs and returns metadata and size information.
// The actual PDF content can be passed to Claude's native document processing.
type Skill struct {
	timeout time.Duration
	client  *http.Client
}

// New creates a new PDF reader skill with default settings.
func New() *Skill {
	timeout := 30 * time.Second
	return &Skill{
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// Name returns the skill name.
func (s *Skill) Name() string { return "pdf_read" }

// Description returns the skill description.
func (s *Skill) Description() string {
	return "Fetch a PDF document from a URL and return its content info"
}

// InputSchema returns the JSON schema for the skill input.
func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "URL of the PDF document to fetch"
			}
		},
		"required": ["url"]
	}`)
}

// Execute fetches the PDF from the given URL and returns information about it.
func (s *Skill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params pdfInput
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("pdf_read: invalid input: %w", err)
	}
	if params.URL == "" {
		return "", fmt.Errorf("pdf_read: url is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "", fmt.Errorf("pdf_read: create request: %w", err)
	}
	req.Header.Set("User-Agent", "iulita-bot/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("pdf_read: fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pdf_read: HTTP %d for %s", resp.StatusCode, params.URL)
	}

	contentType := resp.Header.Get("Content-Type")
	contentLength := resp.ContentLength

	// Read the body to determine actual size if Content-Length is not set.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("pdf_read: read body: %w", err)
	}

	if contentLength < 0 {
		contentLength = int64(len(body))
	}

	result := fmt.Sprintf("PDF fetched from %s\nContent-Type: %s\nSize: %d bytes (%s)\n",
		params.URL,
		contentType,
		contentLength,
		humanSize(contentLength),
	)

	// If the content appears to be a PDF, note that it's available for processing.
	if len(body) > 4 && string(body[:5]) == "%PDF-" {
		result += "Format: Valid PDF document detected.\n"
		result += "The document has been fetched successfully and is available for processing."
	} else {
		// Not a PDF — return the text content directly (up to a limit).
		const maxText = 50000
		text := string(body)
		if len(text) > maxText {
			text = text[:maxText] + "\n... (truncated)"
		}
		result += "Note: Content does not appear to be a PDF. Returning raw content:\n\n" + text
	}

	return result, nil
}

func humanSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
