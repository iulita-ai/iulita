package websearch

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy declares the reviewed native execution policy.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "public-information-v1", Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}
