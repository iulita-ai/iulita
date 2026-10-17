package httpfetch

import (
	"context"

	"github.com/iulita-ai/iulita/internal/skill"
)

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *Skill) NativePolicy() skill.NativePolicy {
	return skill.NativePolicy{Revision: "strict-native-http-v1", Account: true, Interactive: true, InternalConfirmations: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "unknown", Scopes: []string{"assistant:services:read"}}}}
}

// NativeAvailable requires the explicitly assigned owner connection.
func (s *Skill) NativeAvailable(_ context.Context, _ string) bool {
	return s.factory != nil && s.factory.Enabled()
}
