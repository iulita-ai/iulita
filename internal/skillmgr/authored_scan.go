package skillmgr

import (
	"fmt"
	"regexp"
	"strings"
)

// Bounds for self-authored (machine-proposed) skill drafts. These are stricter
// than for human-curated marketplace skills because the content originates from
// an LLM reflecting on a conversation — a prompt-injection sink.
const (
	maxAuthoredBodyLen  = 1500 // characters
	maxAuthoredTriggers = 4
	minTriggerLen       = 4 // shorter words are too generic to force a skill
)

var validAuthoredSlug = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,40}$`)

// genericTriggers are words too broad to safely force a skill — a self-authored
// skill that triggers on these would hijack large swaths of normal conversation.
var genericTriggers = map[string]struct{}{
	"the": {}, "and": {}, "help": {}, "please": {}, "do": {}, "run": {},
	"make": {}, "get": {}, "set": {}, "show": {}, "tell": {}, "give": {},
	"what": {}, "when": {}, "where": {}, "how": {}, "why": {}, "who": {},
	"this": {}, "that": {}, "task": {}, "thing": {}, "stuff": {}, "skill": {},
	"yes": {}, "no": {}, "okay": {}, "now": {}, "today": {}, "create": {},
}

// ScanAuthoredSkill validates a machine-proposed text-only skill draft and
// returns human-readable warnings plus a `blocked` flag. A blocked proposal must
// never be installable: the caller persists it with status "rejected".
//
// This deliberately covers the two live-prompt attack vectors for a text-only
// skill: the body (which would become system-prompt text) and the force-triggers
// (which would auto-activate it). It does NOT register or inject anything.
func ScanAuthoredSkill(slug, name, body string, triggers []string) (warnings []string, blocked bool) {
	if !validAuthoredSlug.MatchString(slug) {
		warnings = append(warnings, fmt.Sprintf("invalid slug %q (need ^[a-z][a-z0-9_-]{2,40}$)", slug))
		blocked = true
	}
	if strings.TrimSpace(name) == "" {
		warnings = append(warnings, "empty name")
		blocked = true
	}

	body = strings.TrimSpace(body)
	if body == "" {
		warnings = append(warnings, "empty body")
		blocked = true
	}
	if len(body) > maxAuthoredBodyLen {
		warnings = append(warnings, fmt.Sprintf("body too long: %d > %d chars", len(body), maxAuthoredBodyLen))
		blocked = true
	}

	// Prompt-injection patterns in the body are a hard block: the body would
	// otherwise land in the static system prompt verbatim.
	if inj := scanForInjection(body); len(inj) > 0 {
		warnings = append(warnings, inj...)
		blocked = true
	}

	if len(triggers) > maxAuthoredTriggers {
		warnings = append(warnings, fmt.Sprintf("too many triggers: %d > %d", len(triggers), maxAuthoredTriggers))
		blocked = true
	}
	for _, raw := range triggers {
		t := strings.ToLower(strings.TrimSpace(raw))
		if t == "" {
			continue
		}
		if len(t) < minTriggerLen {
			warnings = append(warnings, fmt.Sprintf("trigger %q too short (min %d chars)", t, minTriggerLen))
			blocked = true
		}
		if _, generic := genericTriggers[t]; generic {
			warnings = append(warnings, fmt.Sprintf("trigger %q is too generic", t))
			blocked = true
		}
	}

	return warnings, blocked
}
