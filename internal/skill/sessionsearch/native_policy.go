package sessionsearch

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy keeps history local to the current conversation.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "current-history-v1", Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}
