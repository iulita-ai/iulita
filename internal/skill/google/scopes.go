package google

import (
	"encoding/json"
	"strings"

	googlecalendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/tasks/v1"
)

// ScopePresets maps preset names to Google API scope lists.
var ScopePresets = map[string][]string{
	"readonly": {
		gmail.GmailReadonlyScope,
		googlecalendar.CalendarReadonlyScope,
		"https://www.googleapis.com/auth/contacts.readonly",
		tasks.TasksReadonlyScope,
	},
	"readwrite": {
		gmail.GmailModifyScope,
		googlecalendar.CalendarScope,
		"https://www.googleapis.com/auth/contacts",
		tasks.TasksScope,
	},
	"full": {
		gmail.GmailModifyScope,
		googlecalendar.CalendarScope,
		"https://www.googleapis.com/auth/contacts",
		tasks.TasksScope,
		"https://www.googleapis.com/auth/drive",
	},
}

// DefaultScopes returns the default readonly scope set.
func DefaultScopes() []string {
	return ScopePresets["readonly"]
}

// ParseScopesConfig expands a preset name or JSON array into scope URLs.
// Returns the readonly preset if input is empty or unrecognized.
func ParseScopesConfig(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultScopes()
	}

	// Try preset name first.
	if scopes, ok := ScopePresets[strings.ToLower(s)]; ok {
		return scopes
	}

	// Try JSON array.
	var scopes []string
	if err := json.Unmarshal([]byte(s), &scopes); err == nil && len(scopes) > 0 {
		return scopes
	}

	// Unknown value — safe default.
	return DefaultScopes()
}

// FormatScopesForDisplay returns a human-readable scope summary.
func FormatScopesForDisplay(scopes []string) string {
	for name, preset := range ScopePresets {
		if scopesEqual(scopes, preset) {
			return name + " (" + strings.Join(scopeShortNames(scopes), ", ") + ")"
		}
	}
	return "custom (" + strings.Join(scopeShortNames(scopes), ", ") + ")"
}

func scopeShortNames(scopes []string) []string {
	short := make([]string, len(scopes))
	for i, s := range scopes {
		// Extract last path segment.
		parts := strings.Split(s, "/")
		short[i] = parts[len(parts)-1]
	}
	return short
}

func scopesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}
