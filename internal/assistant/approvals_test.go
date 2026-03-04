package assistant

import (
	"testing"

	"golang.org/x/text/language"

	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
)

func TestApprovalStore_SetAndTake(t *testing.T) {
	s := newApprovalStore()

	tc := llm.ToolCall{ID: "tc1", Name: "shell_exec"}
	s.set("chat1", tc, skill.ApprovalManual)

	got, ok := s.take("chat1")
	if !ok {
		t.Fatal("expected pending approval")
	}
	if got.tc.Name != "shell_exec" {
		t.Errorf("tc.Name = %q, want shell_exec", got.tc.Name)
	}
	if got.level != skill.ApprovalManual {
		t.Errorf("level = %d, want ApprovalManual", got.level)
	}

	// Second take should return nothing.
	_, ok = s.take("chat1")
	if ok {
		t.Error("expected no pending after take")
	}
}

func TestApprovalStore_OverwritesPrevious(t *testing.T) {
	s := newApprovalStore()
	s.set("chat1", llm.ToolCall{ID: "tc1", Name: "first"}, skill.ApprovalPrompt)
	s.set("chat1", llm.ToolCall{ID: "tc2", Name: "second"}, skill.ApprovalManual)

	got, ok := s.take("chat1")
	if !ok {
		t.Fatal("expected pending")
	}
	if got.tc.Name != "second" {
		t.Errorf("expected overwritten value, got %q", got.tc.Name)
	}
}

func TestIsApprovalResponse(t *testing.T) {
	// Initialize i18n for locale-aware approval words.
	if err := i18n.Init(); err != nil {
		t.Fatalf("i18n.Init() failed: %v", err)
	}

	tests := []struct {
		text     string
		tag      language.Tag
		approved bool
		defined  bool
	}{
		{"yes", language.English, true, true},
		{"YES", language.English, true, true},
		{"y", language.English, true, true},
		{"confirm", language.English, true, true},
		{"ok", language.English, true, true},
		{"approve", language.English, true, true},
		{"no", language.English, false, true},
		{"NO", language.English, false, true},
		{"n", language.English, false, true},
		{"cancel", language.English, false, true},
		{"deny", language.English, false, true},
		{"reject", language.English, false, true},
		{"maybe", language.English, false, false},
		{"tell me more", language.English, false, false},
		{"  yes  ", language.English, true, true}, // trimmed
		{"", language.English, false, false},
		// Russian locale
		{"да", language.Russian, true, true},
		{"ок", language.Russian, true, true},
		{"нет", language.Russian, false, true},
		{"отмена", language.Russian, false, true},
		{"yes", language.Russian, true, true}, // EN words included in RU catalog
		// Chinese locale
		{"是", language.Chinese, true, true},
		{"好", language.Chinese, true, true},
		{"不", language.Chinese, false, true},
		// Hebrew locale
		{"כן", language.Hebrew, true, true},
		{"לא", language.Hebrew, false, true},
		// Spanish locale
		{"sí", language.Spanish, true, true},
		{"cancelar", language.Spanish, false, true},
		// French locale
		{"oui", language.French, true, true},
		{"non", language.French, false, true},
	}

	for _, tt := range tests {
		approved, defined := isApprovalResponse(tt.text, tt.tag)
		if approved != tt.approved || defined != tt.defined {
			t.Errorf("isApprovalResponse(%q, %v) = (%v, %v), want (%v, %v)",
				tt.text, tt.tag, approved, defined, tt.approved, tt.defined)
		}
	}
}
