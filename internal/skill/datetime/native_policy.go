package datetime

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy declares the reviewed native execution policy.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "public-information-v2", Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}
