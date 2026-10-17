package reminders

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy declares the reviewed native actions and authority requirements.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "native-durable-reminders-v2", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"list": {Effect: "read"}, "create": {Effect: "mutating", Confirmation: true}, "delete": {Effect: "mutating", Confirmation: true}}}
}
