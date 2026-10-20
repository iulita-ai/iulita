package pdfreader

import "github.com/iulita-ai/iulita/internal/skill"

// NativePolicy restricts access to attachments of an authenticated interactive turn.
func (*Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "attached-pdf-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "read"}}}
}
