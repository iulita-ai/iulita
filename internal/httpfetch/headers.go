package httpfetch

import (
	"fmt"
	"net/http"
	"strings"
)

// forbiddenRequestHeaders are transport-managed or hop-by-hop fields the
// model must not set directly (plan §4.2). Content-Length gets a dedicated
// exact-match rule. Host is modeled by Request.Host.
var forbiddenRequestHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"proxy-authorization": true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"te":                  true,
	"trailer":             true, // v1: request trailers unsupported
	"keep-alive":          true,
}

// sensitiveResponseHeaders are masked in every LLM-facing projection.
var sensitiveResponseHeaders = map[string]bool{
	"authorization":       true, // some servers echo the request Authorization
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"set-cookie2":         true,
}

// validateHeaderName rejects malformed names before any network activity.
func validateHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("header name is empty")
	}
	if strings.HasPrefix(name, ":") {
		return fmt.Errorf("HTTP/2 pseudo-headers are not accepted")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !valid {
			return fmt.Errorf("invalid character %q in header name %q", c, name)
		}
	}
	return nil
}

// validateHeaderValue rejects CR/LF/NUL and other control bytes. OBS folding
// (leading SP/HTAB) is rejected as well — values are trimmed before this check.
func validateHeaderValue(name, value string) error {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\r' || c == '\n' || c == 0 {
			return fmt.Errorf("header %q: CR/LF/NUL in value", name)
		}
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("header %q: control character 0x%02x in value", name, c)
		}
	}
	return nil
}

// validateHeaderSpec applies all pre-network header rules to one declared
// header. Returns the canonical lowercase name for conflict tracking.
func validateHeaderSpec(h HeaderSpec, hasAuth, hasCookieJar bool) (string, error) {
	name := strings.TrimSpace(h.Name)
	if err := validateHeaderName(name); err != nil {
		return "", err
	}
	lower := strings.ToLower(name)
	if forbiddenRequestHeaders[lower] {
		return "", fmt.Errorf("header %q is transport-managed and cannot be set", name)
	}
	if lower == "content-length" {
		// Handled separately by the exact-match rule in engine body building.
		return lower, nil
	}
	if len(h.Values) == 0 {
		return "", fmt.Errorf("header %q has no values", name)
	}
	for _, v := range h.Values {
		val := strings.TrimSpace(v.Literal)
		if v.Kind != ValueLiteral {
			val = "<ref>"
		}
		if err := validateHeaderValue(name, val); err != nil {
			return "", err
		}
		if v.Kind != ValueLiteral {
			// Re-check the resolved form later; refs resolve to strings that
			// must pass the same rule at execution time.
			_ = val
		}
	}
	switch lower {
	case "authorization":
		if hasAuth {
			return "", fmt.Errorf("both auth and an Authorization header are set; use one")
		}
	case "cookie":
		if hasCookieJar {
			return "", fmt.Errorf("a manual Cookie header conflicts with cookie_mode:chain")
		}
	}
	return lower, nil
}

// validateMethod checks the HTTP method token. Standard methods are
// normalized to upper case; custom tokens must be valid HTTP tokens.
func validateMethod(method string) (string, error) {
	m := strings.TrimSpace(method)
	if m == "" {
		return "GET", nil
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !valid {
			return "", fmt.Errorf("invalid character %q in method %q", c, m)
		}
	}
	return strings.ToUpper(m), nil
}

// normalizeMethod upper-cases a validated method.
func normalizeMethod(method string) string {
	return strings.ToUpper(strings.TrimSpace(method))
}

// isSafeMethod reports read-only semantics for automatic execution.
func isSafeMethod(method string) bool {
	switch normalizeMethod(method) {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// headerView projects a response header with sensitive masking.
func headerView(name string, values []string) HeaderView {
	sensitive := sensitiveResponseHeaders[strings.ToLower(name)]
	if !sensitive {
		return HeaderView{Name: name, Values: values}
	}
	masked := make([]string, len(values))
	for i := range values {
		masked[i] = "[redacted]"
	}
	return HeaderView{Name: name, Values: masked, Sensitive: true}
}

// lookupHeader finds header values case-insensitively (multivalue-aware).
func lookupHeader(h http.Header, name string) []string {
	if v, ok := h[http.CanonicalHeaderKey(name)]; ok {
		return v
	}
	// http.Header keys are canonical; fall back to a linear scan for unusual
	// spellings that Go may keep verbatim.
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}
