// Package security provides small, reusable safety checks. secretscan blocks
// outgoing text that looks like it contains a credential.
package security

import "regexp"

// secretPatterns is a best-effort (not exhaustive) heuristic. Trivial obfuscation
// (base64, spacing) defeats any regex; the real control is draft approval.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	// Slack tokens incl. app-level (xapp) and browser (xoxc) tokens.
	{"slack_token", regexp.MustCompile(`(?:xox[bpsarce]|xapp)-[0-9A-Za-z-]{10,}`)},
	{"aws_access_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"openai_key", regexp.MustCompile(`sk-(?:proj-)?[0-9A-Za-z_-]{20,}`)},
	{"anthropic_key", regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{20,}`)},
	{"stripe_secret", regexp.MustCompile(`sk_live_[0-9A-Za-z]{20,}`)},
	{"github_token", regexp.MustCompile(`gh[pousr]_[0-9A-Za-z]{20,}|github_pat_[0-9A-Za-z_]{20,}`)},
	{"google_api_key", regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`)},
	{"private_key", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY( BLOCK)?-----`)},
	{"jwt", regexp.MustCompile(`eyJ[0-9A-Za-z_-]{10,}\.eyJ[0-9A-Za-z_-]{10,}\.[0-9A-Za-z_-]{10,}`)},
	// A credential-looking assignment: `api_key = "…"`, `password: …`, `token=…`,
	// bare `key: <value>` — with a non-trivial value. Requires the `[:=]` so it
	// won't fire on prose that merely mentions the word.
	{"credential_assignment", regexp.MustCompile(`(?i)(?:api[_-]?key|secret|passw(?:or)?d|token|access[_-]?key)\s*[:=]\s*\S{8,}`)},
}

// Contains reports whether text likely contains a secret, and the name of the
// first matching pattern (for audit logging — never log the matched substring).
func Contains(text string) (matched bool, pattern string) {
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			return true, p.name
		}
	}
	return false, ""
}
