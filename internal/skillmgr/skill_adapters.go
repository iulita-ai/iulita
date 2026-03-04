package skillmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/iulita-ai/iulita/internal/skill"
)

// textOnlySkill wraps an external text-only skill manifest as a Skill.
// It has no Execute logic — the LLM reads its system prompt.
type textOnlySkill struct {
	manifest *skill.Manifest
}

func newTextOnlySkill(m *skill.Manifest) *textOnlySkill {
	return &textOnlySkill{manifest: m}
}

func (s *textOnlySkill) Name() string                 { return s.manifest.Name }
func (s *textOnlySkill) Description() string          { return s.manifest.Description }
func (s *textOnlySkill) InputSchema() json.RawMessage { return nil }

func (s *textOnlySkill) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("text-only skill %q has no executable component", s.manifest.Name)
}

func (s *textOnlySkill) RequiredCapabilities() []string {
	return s.manifest.Capabilities
}

// executableSkill wraps an external skill that runs via an Executor.
type executableSkill struct {
	manifest   *skill.Manifest
	executor   Executor
	entrypoint string
}

func newExecutableSkill(m *skill.Manifest, exec Executor, entrypoint string) *executableSkill {
	return &executableSkill{
		manifest:   m,
		executor:   exec,
		entrypoint: entrypoint,
	}
}

func (s *executableSkill) Name() string        { return s.manifest.Name }
func (s *executableSkill) Description() string { return s.manifest.Description }

func (s *executableSkill) InputSchema() json.RawMessage {
	// Generic input schema — accept any JSON object.
	return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Input for the skill"}},"additionalProperties":true}`)
}

func (s *executableSkill) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if s.manifest.External == nil {
		return "", fmt.Errorf("not an external skill")
	}
	return s.executor.Execute(ctx, s.manifest.External.InstallDir, s.entrypoint, input, nil)
}

func (s *executableSkill) RequiredCapabilities() []string {
	return s.manifest.Capabilities
}

func (s *executableSkill) ApprovalLevel() skill.ApprovalLevel {
	return s.executor.ApprovalLevel()
}

// webfetchProxySkill wraps a text-only skill that needs HTTP access (curl/wget)
// into a callable tool. When invoked, it fetches URLs extracted from the skill's
// system prompt and returns real data instead of relying on the LLM to call webfetch.
// cliUserAgents maps CLI tool names to appropriate User-Agent strings.
// When a skill requires these tools, we mimic their UA so services
// return CLI-friendly responses (plain text instead of HTML).
var cliUserAgents = map[string]string{
	"curl": "curl/8.0",
	"wget": "Wget/1.21",
}

const browserUserAgent = "Mozilla/5.0 (compatible; Iulita/1.0)"

type webfetchProxySkill struct {
	manifest   *skill.Manifest
	httpClient *http.Client
	urlHints   []string // base URLs extracted from the system prompt
	userAgent  string   // User-Agent header to send (curl-like or browser-like)
}

// urlWithSchemeRe matches URLs with scheme (https://... or http://...).
var urlWithSchemeRe = regexp.MustCompile(`https?://[^\s"'\x60\)]+`)

// urlSchemelessRe matches domain/path patterns without scheme inside quotes or after whitespace.
// Captures patterns like: wttr.in/London?format=3, api.example.com/v1/data
// Requires at least one dot in the domain and a path component.
var urlSchemelessRe = regexp.MustCompile(`(?:^|[\s"'\x60])((?:[a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}/[^\s"'\x60\)]+)`)

// templateVarRe matches {placeholder} patterns in URLs.
var templateVarRe = regexp.MustCompile(`\{[^}]+\}`)

// binaryExtensions are file extensions that produce binary (non-text) responses.
var binaryExtensions = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".pdf", ".zip"}

// urlHostKey returns just the host portion of a URL.
func urlHostKey(rawURL string) string {
	s := rawURL
	if idx := strings.Index(s, "://"); idx != -1 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx != -1 {
		s = s[:idx]
	}
	return s
}

// urlPathKey returns scheme://host/path (without query params) for dedup.
// URLs with the same host+path but different query params are considered
// variants of the same endpoint. We keep the last one seen, since docs
// typically go from simple/compact to full/detailed output.
func urlPathKey(rawURL string) string {
	base := rawURL
	if idx := strings.IndexAny(base, "?#"); idx != -1 {
		base = base[:idx]
	}
	return base
}

func newWebfetchProxySkill(m *skill.Manifest, httpClient *http.Client, requiredBins []string) *webfetchProxySkill {
	// We collect candidates, deduplicating by path. When the same path is seen
	// multiple times with different query params, we keep the LAST one — docs
	// typically progress from compact to full format.
	seenExact := make(map[string]bool)
	pathIndex := make(map[string]int) // path key → index in candidates
	var candidates []string

	addHint := func(match string) {
		match = strings.TrimRight(match, ".,;:")
		if match == "" {
			return
		}
		// Normalize: ensure scheme is present.
		if !strings.HasPrefix(match, "http://") && !strings.HasPrefix(match, "https://") {
			match = "https://" + match
		}
		if seenExact[match] {
			return
		}
		seenExact[match] = true

		// Skip binary file URLs.
		lower := strings.ToLower(match)
		for _, ext := range binaryExtensions {
			if strings.Contains(lower, ext) {
				return
			}
		}
		// Skip documentation/reference pages.
		if strings.Contains(lower, "/docs") || strings.Contains(lower, "/en/docs") {
			return
		}

		// Skip bare path URLs with no query params (likely just example city names
		// like wttr.in/New+York) — they duplicate the parameterized API patterns.
		if !strings.ContainsAny(match, "?=") && !strings.Contains(match, "/v") {
			// Check if we already have a URL with the same host.
			host := urlHostKey(match)
			for _, c := range candidates {
				if strings.Contains(c, host) {
					return // already have a parameterized URL for this host
				}
			}
		}

		// Dedup by host+path: keep the LAST variant (typically the fullest output).
		key := urlPathKey(match)
		if idx, exists := pathIndex[key]; exists {
			candidates[idx] = match // overwrite with later variant
			return
		}
		pathIndex[key] = len(candidates)
		candidates = append(candidates, match)
	}

	// Pass 1: scheme-prefixed URLs.
	for _, match := range urlWithSchemeRe.FindAllString(m.SystemPrompt, -1) {
		addHint(match)
	}
	// Pass 2: scheme-less domain/path patterns (e.g. wttr.in/London?format=3).
	for _, groups := range urlSchemelessRe.FindAllStringSubmatch(m.SystemPrompt, -1) {
		if len(groups) > 1 {
			addHint(groups[1])
		}
	}

	// Pick User-Agent based on required bins: if the skill needs curl/wget,
	// use a matching CLI UA so services return plain text instead of HTML.
	ua := browserUserAgent
	for _, bin := range requiredBins {
		if cliUA, ok := cliUserAgents[bin]; ok {
			ua = cliUA
			break
		}
	}

	return &webfetchProxySkill{
		manifest:   m,
		httpClient: httpClient,
		urlHints:   candidates,
		userAgent:  ua,
	}
}

func (s *webfetchProxySkill) Name() string { return s.manifest.Name }
func (s *webfetchProxySkill) Description() string {
	return s.manifest.Description
}

func (s *webfetchProxySkill) InputSchema() json.RawMessage {
	desc := fmt.Sprintf("Query for the %s skill. Provide only the essential search term (e.g. a name, keyword, or identifier). Do NOT include time modifiers like 'tomorrow' or 'next week'.", s.manifest.Name)
	schema := fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": %q
			}
		},
		"required": ["query"]
	}`, desc)
	return json.RawMessage(schema)
}

func (s *webfetchProxySkill) RequiredCapabilities() []string {
	return s.manifest.Capabilities
}

const maxProxyResponseLen = 8000

func (s *webfetchProxySkill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	// Build candidate URLs from the system prompt hints.
	urls := s.buildURLs(in.Query)
	if len(urls) == 0 {
		return "No URL patterns found in skill instructions. " +
			"Please use the webfetch tool directly with the appropriate URL.", nil
	}

	// Try each URL until one works (limit to first 3).
	var lastErr error
	limit := 3
	if len(urls) < limit {
		limit = len(urls)
	}
	for _, u := range urls[:limit] {
		result, err := s.fetch(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		return fmt.Sprintf("<<<SKILL_DATA source=%q url=%q>>>\n%s\n<<<END_SKILL_DATA>>>", s.manifest.Name, u, result), nil
	}

	return "", fmt.Errorf("all URL patterns failed (tried %d of %d), last error: %w", limit, len(urls), lastErr)
}

// stripTemporalModifiers removes common time-related words that the LLM
// might include in the query but that aren't part of the location name.
func stripTemporalModifiers(query string) string {
	// Split, filter, rejoin.
	words := strings.Fields(query)
	var filtered []string
	for _, w := range words {
		lower := strings.ToLower(w)
		if temporalWords[lower] {
			continue
		}
		filtered = append(filtered, w)
	}
	if len(filtered) == 0 {
		return query // don't strip everything
	}
	return strings.Join(filtered, " ")
}

var temporalWords = map[string]bool{
	// English
	"today": true, "tomorrow": true, "yesterday": true,
	"week": true, "weekend": true, "month": true,
	"next": true, "this": true, "last": true,
	"forecast": true, "current": true, "now": true,
	// Russian
	"сегодня": true, "завтра": true, "вчера": true,
	"неделю": true, "неделя": true, "недели": true,
	"месяц": true, "выходные": true,
	"следующую": true, "следующий": true, "следующая": true,
	"текущий": true, "текущая": true, "текущую": true,
	"эту": true, "этот": true, "эта": true,
	"на": true, "в": true, "за": true,
	"прогноз": true, "погода": true, "погоду": true,
}

// buildURLs constructs concrete URLs by substituting the query into URL patterns.
func (s *webfetchProxySkill) buildURLs(query string) []string {
	// Strip temporal modifiers (e.g. "London tomorrow" → "London").
	query = stripTemporalModifiers(query)
	// URL-safe query: replace spaces with +.
	safeQuery := strings.ReplaceAll(query, " ", "+")

	var urls []string
	for _, hint := range s.urlHints {
		// 1) Replace {placeholder} template variables (e.g. {location}, {city}).
		if templateVarRe.MatchString(hint) {
			urls = append(urls, templateVarRe.ReplaceAllString(hint, safeQuery))
			continue
		}

		// 2) Replace known city/location names in the URL.
		u := replaceLocationInURL(hint, safeQuery)
		if u != hint {
			urls = append(urls, u)
			continue
		}

		// 3) Skip documentation/reference URLs that can't be parameterized.
	}

	return urls
}

// replaceLocationInURL replaces known city/location names in a URL with the query.
var commonCities = []string{
	"London", "london", "Berlin", "berlin", "Moscow", "moscow",
	"Paris", "paris", "Tokyo", "tokyo", "New+York", "JFK",
}

func replaceLocationInURL(url, query string) string {
	for _, city := range commonCities {
		if strings.Contains(url, city) {
			return strings.Replace(url, city, query, 1)
		}
	}
	return url
}

func (s *webfetchProxySkill) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", s.userAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	text := string(body)
	if len(text) > maxProxyResponseLen {
		text = text[:maxProxyResponseLen] + "\n...(truncated)"
	}
	return text, nil
}
