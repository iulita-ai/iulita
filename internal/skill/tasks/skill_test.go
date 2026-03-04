package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name   string
	result string
	err    error
	lastIn json.RawMessage
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Execute(_ context.Context, input json.RawMessage) (string, error) {
	m.lastIn = input
	return m.result, m.err
}

// mockRegistry implements capabilityChecker for testing.
type mockRegistry struct {
	available map[string]bool
}

func (r *mockRegistry) Get(name string) (skill.Skill, bool) {
	if r.available[name] {
		return nil, true // We only need the bool for capability checking.
	}
	return nil, false
}

func setupSkill(providers ...*mockProvider) (*Skill, *mockRegistry) {
	reg := &mockRegistry{available: make(map[string]bool)}
	s := NewSkill(reg, zap.NewNop())
	for _, p := range providers {
		reg.available[p.name] = true
		s.RegisterProvider(p.name, "", p)
	}
	return s, reg
}

func TestSkillMetadata(t *testing.T) {
	s := NewSkill(&mockRegistry{}, zap.NewNop())

	if s.Name() != "tasks" {
		t.Errorf("got name %q, want %q", s.Name(), "tasks")
	}

	caps := s.RequiredCapabilities()
	if caps != nil {
		t.Errorf("meta-skill should have no required capabilities, got %v", caps)
	}

	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Fatalf("invalid schema: %v", err)
	}
}

func TestNoProviders(t *testing.T) {
	s := NewSkill(&mockRegistry{available: make(map[string]bool)}, zap.NewNop())
	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"overview"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No task providers") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestOverview(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Tasks (2):\n1. Buy milk\n2. Call mom"}
	gtasks := &mockProvider{name: "google_tasks", result: "Tasks (1):\n1. Review PR"}
	s, _ := setupSkill(todoist, gtasks)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"overview"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Task Overview") {
		t.Error("should contain overview header")
	}
	if !strings.Contains(result, "todoist") {
		t.Error("should contain todoist section")
	}
	if !strings.Contains(result, "google_tasks") {
		t.Error("should contain google_tasks section")
	}
	if !strings.Contains(result, "Buy milk") {
		t.Error("should contain todoist tasks")
	}
	if !strings.Contains(result, "Review PR") {
		t.Error("should contain google tasks")
	}
}

func TestListWithProvider(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Tasks (1):\n1. Task"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list","provider":"todoist"}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Task") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestListWithoutProvider(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Tasks"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Without provider, behaves like overview.
	if !strings.Contains(result, "Task Overview") {
		t.Errorf("expected overview, got: %s", result)
	}
}

func TestCreate(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Task created: Buy milk [id: 123]"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{
		"action": "create",
		"provider": "todoist",
		"content": "Buy milk",
		"due_string": "tomorrow",
		"priority": "P1"
	}`))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "[todoist]") {
		t.Error("should prefix with provider name")
	}
	if !strings.Contains(result, "Task created") {
		t.Errorf("unexpected result: %s", result)
	}

	// Check forwarded input contains todoist-specific fields.
	var forwarded map[string]any
	json.Unmarshal(todoist.lastIn, &forwarded)
	if forwarded["action"] != "create" {
		t.Errorf("forwarded action = %v", forwarded["action"])
	}
	if forwarded["due_string"] != "tomorrow" {
		t.Errorf("forwarded due_string = %v", forwarded["due_string"])
	}
	if forwarded["priority"] != "P1" {
		t.Errorf("forwarded priority = %v", forwarded["priority"])
	}
}

func TestCreateNoContent(t *testing.T) {
	s, _ := setupSkill(&mockProvider{name: "todoist", result: ""})
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create","provider":"todoist"}`))
	if err == nil {
		t.Fatal("expected error for missing content")
	}
}

func TestCreateDefaultProvider(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Task created"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"create","content":"Test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "[todoist]") {
		t.Error("should default to first available provider")
	}
}

func TestComplete(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Task 42 completed"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{
		"action": "complete",
		"provider": "todoist",
		"task_id": "42"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "[todoist]") {
		t.Error("should prefix with provider name")
	}
}

func TestCompleteNoProvider(t *testing.T) {
	s, _ := setupSkill(&mockProvider{name: "todoist", result: ""})
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"complete","task_id":"42"}`))
	if err == nil {
		t.Fatal("expected error for missing provider")
	}
}

func TestCompleteNoTaskID(t *testing.T) {
	s, _ := setupSkill(&mockProvider{name: "todoist", result: ""})
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"complete","provider":"todoist"}`))
	if err == nil {
		t.Fatal("expected error for missing task_id")
	}
}

func TestProviderAction(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: "Projects (3)"}
	s, _ := setupSkill(todoist)

	result, err := s.Execute(context.Background(), json.RawMessage(`{
		"action": "provider",
		"provider": "todoist",
		"provider_input": {"action": "projects"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Projects") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestProviderActionNoInput(t *testing.T) {
	s, _ := setupSkill(&mockProvider{name: "todoist", result: ""})
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"provider","provider":"todoist"}`))
	if err == nil {
		t.Fatal("expected error for missing provider_input")
	}
}

func TestUnavailableProvider(t *testing.T) {
	todoist := &mockProvider{name: "todoist", result: ""}
	s, reg := setupSkill(todoist)
	reg.available["todoist"] = false // Disable it.

	result, err := s.Execute(context.Background(), json.RawMessage(`{"action":"overview"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No task providers") {
		t.Errorf("should report no providers when all disabled: %s", result)
	}
}

func TestUnknownAction(t *testing.T) {
	s, _ := setupSkill(&mockProvider{name: "todoist", result: ""})
	_, err := s.Execute(context.Background(), json.RawMessage(`{"action":"fly"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("manifest is nil")
	}
	if m.Name != "tasks" {
		t.Errorf("got name %q, want %q", m.Name, "tasks")
	}
	if m.SystemPrompt == "" {
		t.Error("system prompt should not be empty")
	}
}
