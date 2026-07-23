package slack

import "strings"

// HasWriteScope reports whether a comma-separated granted-scope string contains
// any write scope. Used to fail a connect CLOSED if Slack ever returns a write
// scope — the token must be provably read-only even if the app is misconfigured.
func HasWriteScope(granted string) bool {
	for _, s := range strings.Split(granted, ",") {
		if strings.Contains(strings.TrimSpace(s), "write") {
			return true
		}
	}
	return false
}

// RequiredUserScopes are the Slack USER-token scopes requested for the owner's
// personal connection — enough to search and read everything the owner can see.
// This is a single fixed set (unlike Google's readonly/readwrite/full presets):
// the connection is read-only by design and never requests write scopes.
func RequiredUserScopes() []string {
	return []string{
		"search:read",      // search.messages (user-token only) — the enabler
		"channels:history", // read public channel messages
		"channels:read",    // list/resolve public channels
		"groups:history",   // read private channels the owner is in
		"groups:read",      // list/resolve private channels
		"users:read",       // resolve user names
	}
}
