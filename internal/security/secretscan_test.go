package security

import "testing"

func TestContains(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    bool
		pattern string
	}{
		{"slack bot token", "here is the token xoxb-abcdefghij-secretpart", true, "slack_token"}, // gitleaks:allow (test fixture)
		{"slack app token", "app xapp-1-abcdefghijklmnop here", true, "slack_token"},             // gitleaks:allow (test fixture)
		{"aws key", "creds AKIAIOSFODNN7EXAMPLE here", true, "aws_access_key"},                   // gitleaks:allow (test fixture)
		{"openai bare", "the key is sk-proj-abcdefghijklmnopqrstuvwx now", true, "openai_key"},   // gitleaks:allow (test fixture)
		{"github bare", "use ghp_abcdefghijklmnopqrstuv1234 for ci", true, "github_token"},       // gitleaks:allow (test fixture)
		{"google bare", "AIzaSyAabcdefghijklmnopqrstuvwxyz0123456 key", true, "google_api_key"},  // gitleaks:allow (test fixture)
		{"stripe bare", "sk_live_abcdefghijklmnopqrstuv here", true, "stripe_secret"},            // gitleaks:allow (test fixture)
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIabc", true, "private_key"},          // gitleaks:allow (test fixture)
		{"jwt", "auth eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4", true, "jwt"},        // gitleaks:allow (test fixture)
		{"credential assignment", `password = hunter2xyz`, true, "credential_assignment"},        // gitleaks:allow (test fixture)
		{"api_key assignment", `api_key: sk-abcdef123456`, true, "credential_assignment"},        // gitleaks:allow (test fixture)
		{"prose mentioning token", "I'll grab you an access token from the meeting notes", false, ""},
		{"prose mentioning password", "Please reset your password soon.", false, ""},
		{"short assignment value", `token = abc`, false, ""}, // value under 8 chars
		{"ordinary message", "Deploy finished, all green. Ready for review.", false, ""},
		{"empty", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, pat := Contains(tt.text)
			if got != tt.want {
				t.Errorf("Contains(%q) matched=%v, want %v (pattern %q)", tt.text, got, tt.want, pat)
			}
			if got && tt.pattern != "" && pat != tt.pattern {
				t.Errorf("Contains(%q) pattern=%q, want %q", tt.text, pat, tt.pattern)
			}
		})
	}
}
