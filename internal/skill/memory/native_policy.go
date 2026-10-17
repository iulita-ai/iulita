package memory

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy declares the reviewed native execution policy.
func (*RememberSkill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "account-memory-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "mutating"}}}
}

// NativePolicy declares the reviewed native execution policy.
func (*RecallSkill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "account-memory-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}

// NativePolicy declares the reviewed native execution policy.
func (*ForgetSkill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "account-memory-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "mutating", Confirmation: true}}}
}
