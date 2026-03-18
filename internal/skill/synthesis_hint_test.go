package skill_test

import (
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/datetime"
	"github.com/iulita-ai/iulita/internal/skill/exchange"
	"github.com/iulita-ai/iulita/internal/skill/geolocation"
	"github.com/iulita-ai/iulita/internal/skill/insights"
	"github.com/iulita-ai/iulita/internal/skill/memory"
	"github.com/iulita-ai/iulita/internal/skill/websearch"
)

// TestCheapSkillsSatisfySynthesisModelDeclarer verifies that all skills marked
// as cheap-synthesis correctly implement the SynthesisModelDeclarer interface
// and return RouteHintCheap.
func TestCheapSkillsSatisfySynthesisModelDeclarer(t *testing.T) {
	cheapSkills := []struct {
		name  string
		skill skill.Skill
	}{
		{"datetime", datetime.New()},
		{"exchange_rate", exchange.New(nil)},
		{"geolocation", geolocation.New(nil)},
		{"recall", memory.NewRecall(nil)},
		{"list_insights", insights.NewList(nil)},
		{"websearch", websearch.New(nil, nil)},
	}

	for _, tc := range cheapSkills {
		t.Run(tc.name, func(t *testing.T) {
			smd, ok := tc.skill.(skill.SynthesisModelDeclarer)
			if !ok {
				t.Fatalf("%s does not implement SynthesisModelDeclarer", tc.name)
			}
			hint := smd.SynthesisRouteHint()
			if hint != llm.RouteHintCheap {
				t.Errorf("%s.SynthesisRouteHint() = %q, want %q", tc.name, hint, llm.RouteHintCheap)
			}
		})
	}
}

// TestExpensiveSkillsDoNotDeclare verifies that skills requiring full model
// do NOT implement SynthesisModelDeclarer.
func TestExpensiveSkillsDoNotDeclare(t *testing.T) {
	expensive := []struct {
		name  string
		skill skill.Skill
	}{
		{"remember", memory.NewRemember(nil)},
		{"dismiss_insight", insights.NewDismiss(nil)},
		{"promote_insight", insights.NewPromote(nil)},
	}
	for _, tc := range expensive {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.skill.(skill.SynthesisModelDeclarer); ok {
				t.Errorf("%s should NOT implement SynthesisModelDeclarer", tc.name)
			}
		})
	}
}
