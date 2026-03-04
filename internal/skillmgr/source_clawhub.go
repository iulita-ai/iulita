package skillmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	clawhubBaseURL     = "https://clawhub.ai/api/v1"
	clawhubDownloadMax = 50 << 20 // 50 MB
	clawhubMaxRetries  = 3
)

// ClawhHubSource implements Source for the ClawhHub marketplace.
type ClawhHubSource struct {
	baseURL    string
	token      string
	httpClient *http.Client
	log        *zap.Logger
}

// NewClawhHubSource creates a ClawhHub marketplace source.
func NewClawhHubSource(token string, httpClient *http.Client, log *zap.Logger) *ClawhHubSource {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &ClawhHubSource{
		baseURL:    clawhubBaseURL,
		token:      token,
		httpClient: httpClient,
		log:        log,
	}
}

func (s *ClawhHubSource) Name() string { return "clawhub" }

// doWithRetry executes an HTTP request, retrying on 429 responses using the
// Retry-After header. Returns the successful response (caller must close body).
// Only safe for requests without a body (GET/HEAD) — body is not rewound on retry.
func (s *ClawhHubSource) doWithRetry(req *http.Request) (*http.Response, error) {
	for attempt := 0; attempt <= clawhubMaxRetries; attempt++ {
		if attempt > 0 {
			req = req.Clone(req.Context())
		}
		s.log.Debug("clawhub request",
			zap.String("method", req.Method),
			zap.String("url", req.URL.String()),
			zap.Int("attempt", attempt+1),
		)
		resp, err := s.httpClient.Do(req)
		if err != nil {
			s.log.Debug("clawhub request failed", zap.String("url", req.URL.String()), zap.Error(err))
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			s.log.Debug("clawhub response", zap.String("url", req.URL.String()), zap.Int("status", resp.StatusCode))
			return resp, nil
		}
		resp.Body.Close()

		if attempt == clawhubMaxRetries {
			s.log.Warn("clawhub rate limit exhausted",
				zap.String("url", req.URL.String()),
				zap.Int("retries", clawhubMaxRetries),
			)
			return nil, fmt.Errorf("rate limited after %d retries", clawhubMaxRetries)
		}

		wait := 5 * time.Second
		// Prefer X-RateLimit-Reset (absolute Unix timestamp) over Retry-After
		// (relative seconds) — ClawhHub's Retry-After can be too short while
		// the rate limit window is still active.
		if resetStr := resp.Header.Get("X-Ratelimit-Reset"); resetStr != "" {
			if resetTS, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
				untilReset := time.Until(time.Unix(resetTS, 0)) + time.Second
				if untilReset > 0 && untilReset <= 120*time.Second {
					wait = untilReset
				} else if untilReset > 120*time.Second {
					s.log.Warn("clawhub rate limit reset too far, using default",
						zap.Duration("until_reset", untilReset),
						zap.Duration("using", wait),
					)
				}
			}
		} else if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				if secs <= 120 {
					wait = time.Duration(secs)*time.Second + time.Second
				} else {
					s.log.Warn("clawhub Retry-After exceeds cap, using default",
						zap.Int("server_requested_secs", secs),
						zap.Duration("using", wait),
					)
				}
			}
		}

		s.log.Info("clawhub rate limited, waiting",
			zap.String("url", req.URL.String()),
			zap.Duration("wait", wait),
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", clawhubMaxRetries),
		)

		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return nil, fmt.Errorf("rate limited") // unreachable
}

// clawhubSkillItem represents a skill in the search list response.
type clawhubSkillItem struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	Summary       string `json:"summary"`
	LatestVersion struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
	Stats struct {
		Downloads int `json:"downloads"`
		Stars     int `json:"stars"`
	} `json:"stats"`
}

// clawhubListResponse matches the ClawhHub list endpoint (/skills).
type clawhubListResponse struct {
	Items      []clawhubSkillItem `json:"items"`
	NextCursor string             `json:"nextCursor"`
}

// clawhubSearchResult represents a single result from the search endpoint.
type clawhubSearchResult struct {
	Score       float64 `json:"score"`
	Slug        string  `json:"slug"`
	DisplayName string  `json:"displayName"`
	Summary     string  `json:"summary"`
	Version     *string `json:"version"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// clawhubSearchResponse matches the ClawhHub search endpoint (/search).
type clawhubSearchResponse struct {
	Results []clawhubSearchResult `json:"results"`
}

// clawhubSkillDetail represents the GET /skills/:slug response.
type clawhubSkillDetail struct {
	Skill struct {
		Slug        string `json:"slug"`
		DisplayName string `json:"displayName"`
		Summary     string `json:"summary"`
	} `json:"skill"`
	LatestVersion struct {
		Version string `json:"version"`
	} `json:"latestVersion"`
	Owner struct {
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"owner"`
	Stats struct {
		Downloads int `json:"downloads"`
		Stars     int `json:"stars"`
	} `json:"stats"`
}

// extractSlugFromRef extracts a slug from various ref formats:
//   - "weather" → "weather"
//   - "clawhub/weather" → "weather"
//   - "steipete/weather" → "weather" (owner/slug)
//   - "https://clawhub.ai/steipete/weather" → "weather"
func (s *ClawhHubSource) extractSlugFromRef(ref string) string {
	// Handle full ClawhHub web URLs (HTTPS only).
	if strings.HasPrefix(ref, "https://clawhub.ai/") {
		parsed, err := url.Parse(ref)
		if err == nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) >= 2 {
				return parts[len(parts)-1] // last segment = slug
			}
			if len(parts) == 1 && parts[0] != "" {
				return parts[0]
			}
		}
	}
	// Strip "clawhub/" prefix.
	slug := strings.TrimPrefix(ref, "clawhub/")
	// Handle "owner/slug" format — take last part.
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		slug = slug[idx+1:]
	}
	return slug
}

// downloadURL constructs the download URL for a slug.
func (s *ClawhHubSource) downloadURL(slug string) string {
	return fmt.Sprintf("%s/download?slug=%s", s.baseURL, url.QueryEscape(slug))
}

func (s *ClawhHubSource) Resolve(ctx context.Context, ref string) (*SkillRef, error) {
	slug := s.extractSlugFromRef(ref)
	s.log.Debug("resolving skill", zap.String("ref", ref), zap.String("slug", slug))

	u := fmt.Sprintf("%s/skills/%s", s.baseURL, url.PathEscape(slug))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("fetch skill %q: %w", slug, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		s.log.Debug("resolve failed", zap.String("slug", slug), zap.Int("status", resp.StatusCode))
		return nil, fmt.Errorf("clawhub API: status %d: %s", resp.StatusCode, string(body))
	}

	var detail clawhubSkillDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	s.log.Debug("resolved skill",
		zap.String("slug", detail.Skill.Slug),
		zap.String("version", detail.LatestVersion.Version),
		zap.String("author", detail.Owner.Handle),
	)

	return &SkillRef{
		Slug:             detail.Skill.Slug,
		Name:             detail.Skill.DisplayName,
		Version:          detail.LatestVersion.Version,
		Description:      detail.Skill.Summary,
		Author:           detail.Owner.Handle,
		OwnerDisplayName: detail.Owner.DisplayName,
		DownloadURL:      s.downloadURL(detail.Skill.Slug),
		Source:           "clawhub",
		SourceRef:        fmt.Sprintf("clawhub/%s", detail.Skill.Slug),
		Downloads:        detail.Stats.Downloads,
		Stars:            detail.Stats.Stars,
	}, nil
}

func (s *ClawhHubSource) Download(ctx context.Context, ref *SkillRef, destDir string) (string, string, error) {
	if ref.DownloadURL == "" {
		return "", "", fmt.Errorf("no download URL for skill %q", ref.Slug)
	}

	s.log.Debug("downloading skill archive", zap.String("slug", ref.Slug), zap.String("url", ref.DownloadURL))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.DownloadURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("create download request: %w", err)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.doWithRetry(req)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.log.Debug("download failed", zap.String("slug", ref.Slug), zap.Int("status", resp.StatusCode))
		return "", "", fmt.Errorf("download: status %d", resp.StatusCode)
	}

	archivePath := filepath.Join(destDir, ref.Slug+".zip")
	out, err := os.Create(archivePath)
	if err != nil {
		return "", "", fmt.Errorf("create file: %w", err)
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, clawhubDownloadMax+1))
	out.Close()
	if err != nil {
		return "", "", fmt.Errorf("write archive: %w", err)
	}
	if n > clawhubDownloadMax {
		os.Remove(archivePath)
		return "", "", fmt.Errorf("archive exceeds max size (%d bytes)", clawhubDownloadMax)
	}

	s.log.Debug("downloaded skill archive", zap.String("slug", ref.Slug), zap.Int64("bytes", n))

	checksum, err := VerifyChecksum(archivePath, "")
	if err != nil {
		os.Remove(archivePath)
		return "", "", err
	}

	return archivePath, checksum, nil
}

func (s *ClawhHubSource) Search(ctx context.Context, query string, limit int) ([]SkillRef, error) {
	if limit <= 0 {
		limit = 20
	}

	s.log.Debug("searching skills", zap.String("query", query), zap.Int("limit", limit))

	// Use the dedicated search endpoint (/search) which supports BM25 relevance scoring.
	// The list endpoint (/skills) ignores the q parameter and returns by recency.
	u := fmt.Sprintf("%s/search?q=%s&limit=%d", s.baseURL, url.QueryEscape(query), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.doWithRetry(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		s.log.Debug("search failed", zap.String("query", query), zap.Int("status", resp.StatusCode))
		return nil, fmt.Errorf("clawhub search: status %d: %s", resp.StatusCode, string(body))
	}

	var result clawhubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode search: %w", err)
	}

	s.log.Debug("search completed", zap.String("query", query), zap.Int("results", len(result.Results)))

	refs := make([]SkillRef, 0, len(result.Results))
	for _, sk := range result.Results {
		version := ""
		if sk.Version != nil {
			version = *sk.Version
		}
		refs = append(refs, SkillRef{
			Slug:        sk.Slug,
			Name:        sk.DisplayName,
			Version:     version,
			Description: sk.Summary,
			DownloadURL: s.downloadURL(sk.Slug),
			Source:      "clawhub",
			SourceRef:   fmt.Sprintf("clawhub/%s", sk.Slug),
			UpdatedAt:   sk.UpdatedAt,
		})
	}

	return refs, nil
}

// IsClawhHubURL returns true if the URL points to clawhub.ai (HTTPS only).
func IsClawhHubURL(ref string) bool {
	return strings.HasPrefix(ref, "https://clawhub.ai/")
}
