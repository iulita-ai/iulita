package assistanttools

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	httpengine "github.com/iulita-ai/iulita/internal/httpfetch"
)

// Preserve the structured outcome and every artifact reference when shrinking
// inline previews; never cut JSON or present a partial envelope as success.
func compactHTTPResult(raw string) (string, error) {
	if len(raw) <= 60<<10 {
		return raw, nil
	}
	var result httpengine.RunResult
	if json.Unmarshal([]byte(raw), &result) != nil {
		return "", errors.New("invalid HTTP result")
	}
	for n := range result.Steps {
		s := &result.Steps[n]
		if s.Body != nil {
			s.Body.Text = previewText(s.Body.Text, 512)
			s.Body.Base64 = ""
			s.Body.Truncated = true
		}
		if len(s.Headers) > 10 {
			s.Headers = s.Headers[:10]
		}
		for n := range s.Headers {
			h := &s.Headers[n]
			if len(h.Values) > 2 {
				h.Values = h.Values[:2]
			}
			for j := range h.Values {
				h.Values[j] = previewText(h.Values[j], 128)
			}
		}
		s.Trailers = nil
		s.URL = previewText(s.URL, 1024)
		for j := range s.Redirects {
			s.Redirects[j].URL = previewText(s.Redirects[j].URL, 512)
			s.Redirects[j].Location = previewText(s.Redirects[j].Location, 512)
		}
	}
	body, err := json.Marshal(result)
	if err != nil || len(body) > 60<<10 {
		return "", errors.New("HTTP result exceeds native tool envelope")
	}
	return string(body), nil
}
func previewText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
