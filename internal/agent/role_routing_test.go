package agent

import (
	"context"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
	"go.uber.org/zap"
	"testing"
)

type snapshotCapture struct {
	*capturingProvider
	snapshot *llm.ProfileSnapshot
}

func (p *snapshotCapture) AcquireProfileSnapshot() *llm.ProfileSnapshot { return p.snapshot }

func TestPlannerComplexRoleOnlyWithActivePolicy(t *testing.T) {
	profile := models.Profile{ID: "main", Name: "Main", Connection: "claude", Model: "claude-fixture", Thinking: "disabled", MaxOutputTokens: 100}
	snapshot, err := llm.NewProfileSnapshot(1, models.Settings{SchemaVersion: 1, Profiles: []models.Profile{profile}, Policy: models.Policy{Everyday: "main"}}, map[string]llm.ProfileBinding{"main": {Profile: profile, Provider: &stubProvider{}, Eligibility: "legacy_preserved"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		snapshot         *llm.ProfileSnapshot
		role, hint, want string
	}{{nil, "", "", ""}, {snapshot, "", "", "complex"}, {snapshot, "background", "", "background"}, {snapshot, "", "light", ""}} {
		captured := "unset"
		provider := &snapshotCapture{capturingProvider: &capturingProvider{inner: &stubProvider{responses: []llm.Response{{Content: "plan"}}}, onComplete: func(req llm.Request) { captured = req.Role }}, snapshot: test.snapshot}
		runner := NewRunner(provider, newTestRegistry(), nil, nil, "chat", zap.NewNop())
		res := runner.Run(context.Background(), AgentSpec{ID: "planner", Type: AgentTypePlanner, Task: "plan", Role: test.role, RouteHint: test.hint}, Budget{}, nil)
		if res.Err != nil || captured != test.want {
			t.Fatalf("role=%q hint=%q: got %q err=%v want=%q", test.role, test.hint, captured, res.Err, test.want)
		}
	}
}
