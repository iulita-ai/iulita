package skill

import (
	"context"
	"encoding/json"
	"testing"
)

var _ Skill = (*dummySkill)(nil)

func TestApprovalLevel_Constants(t *testing.T) {
	// Verify ordering: Auto < Prompt < Manual.
	if ApprovalAuto >= ApprovalPrompt {
		t.Error("ApprovalAuto should be less than ApprovalPrompt")
	}
	if ApprovalPrompt >= ApprovalManual {
		t.Error("ApprovalPrompt should be less than ApprovalManual")
	}
}

type mockApprovalSkill struct {
	level ApprovalLevel
}

func (m *mockApprovalSkill) ApprovalLevel() ApprovalLevel { return m.level }

func TestApprovalDeclarer_Interface(t *testing.T) {
	s := &mockApprovalSkill{level: ApprovalManual}
	var ad ApprovalDeclarer = s
	if ad.ApprovalLevel() != ApprovalManual {
		t.Errorf("expected ApprovalManual, got %d", ad.ApprovalLevel())
	}
}

func TestApprovalLevelFor_Registry(t *testing.T) {
	r := NewRegistry()

	// Skill without ApprovalDeclarer defaults to Auto.
	r.Register(&dummySkill{name: "reader"})
	if l := r.ApprovalLevelFor("reader"); l != ApprovalAuto {
		t.Errorf("expected ApprovalAuto for plain skill, got %d", l)
	}

	// Unknown skill defaults to Auto.
	if l := r.ApprovalLevelFor("nonexistent"); l != ApprovalAuto {
		t.Errorf("expected ApprovalAuto for unknown, got %d", l)
	}
}

// dummySkill is a minimal Skill implementation for testing.
type dummySkill struct {
	name string
}

func (d *dummySkill) Name() string                                                 { return d.name }
func (d *dummySkill) Description() string                                          { return "test" }
func (d *dummySkill) InputSchema() json.RawMessage                                 { return json.RawMessage(`{}`) }
func (d *dummySkill) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }
