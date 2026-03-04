package skillmgr

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// URLSource implements Source for direct URL downloads.
type URLSource struct {
	httpClient *http.Client
}

// NewURLSource creates a URL-based source.
func NewURLSource(httpClient *http.Client) *URLSource {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &URLSource{httpClient: httpClient}
}

func (s *URLSource) Name() string { return "url" }

func (s *URLSource) Resolve(_ context.Context, ref string) (*SkillRef, error) {
	if !strings.HasPrefix(ref, "https://") {
		return nil, fmt.Errorf("only HTTPS URLs are allowed: %q", ref)
	}

	slug := deriveSlugFromURL(ref)

	// Sanitize slug to prevent path traversal — defense in depth
	// (Manager.Install also validates, but Download uses slug before that).
	slug = filepath.Base(slug)
	if slug == "." || slug == "/" || slug == "" {
		return nil, fmt.Errorf("cannot derive skill slug from URL %q", ref)
	}

	return &SkillRef{
		Slug:        slug,
		Name:        slug,
		DownloadURL: ref,
		Source:      "url",
		SourceRef:   ref,
	}, nil
}

// deriveSlugFromURL extracts a clean slug from a URL.
// Handles query parameters (e.g. ?slug=weather) and path-based slugs.
func deriveSlugFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil {
		// Check for explicit slug query parameter.
		if s := parsed.Query().Get("slug"); s != "" {
			return s
		}
		// Check for name query parameter.
		if s := parsed.Query().Get("name"); s != "" {
			return s
		}
	}

	// Fall back to path-based slug derivation.
	// Use only the path component (strip query string).
	p := rawURL
	if parsed != nil {
		p = parsed.Path
	}

	slug := path.Base(strings.TrimSuffix(p, ".zip"))
	slug = strings.TrimSuffix(slug, "-main") // GitHub archive convention
	slug = strings.TrimSuffix(slug, "-master")

	return slug
}

func (s *URLSource) Download(ctx context.Context, ref *SkillRef, destDir string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.DownloadURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download: status %d", resp.StatusCode)
	}

	archivePath := filepath.Join(destDir, ref.Slug+".zip")
	out, err := os.Create(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("create file: %w", err)
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, maxArchiveSize+1))
	out.Close() // close before potential Remove
	if err != nil {
		return "", "", fmt.Errorf("write: %w", err)
	}
	if n > maxArchiveSize {
		os.Remove(archivePath)
		return "", "", fmt.Errorf("archive exceeds max size (%d bytes)", maxArchiveSize)
	}

	checksum, err := VerifyChecksum(archivePath, "")
	if err != nil {
		return "", "", err
	}

	return archivePath, checksum, nil
}

func (s *URLSource) Search(_ context.Context, _ string, _ int) ([]SkillRef, error) {
	return nil, fmt.Errorf("URL source does not support search")
}
