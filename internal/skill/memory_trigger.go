package skill

import "strings"

// MatchMemoryTrigger shares explicit memory intent matching between channels.
func MatchMemoryTrigger(text string, triggers []string) bool {
	lower := strings.ToLower(text)
	for _, trigger := range triggers {
		if trigger != "" && strings.Contains(lower, strings.ToLower(trigger)) {
			return true
		}
	}
	return false
}
