package directives

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy declares the reviewed native execution policy.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "account-directives-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"get": {Effect: "read"}, "set": {Effect: "mutating", Confirmation: true}, "clear": {Effect: "mutating", Confirmation: true}}}
}
