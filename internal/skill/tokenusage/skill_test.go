package tokenusage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	if err := store.RunMigrations(ctx); err != nil {
		t.Fatalf("running migrations: %v", err)
	}
	if err := store.CreateVectorTables(ctx); err != nil {
		t.Fatalf("creating vector tables: %v", err)
	}
	return store
}

func TestTokenStatsSkill_Metadata(t *testing.T) {
	s := New(nil)
	if s.Name() != "token_stats" {
		t.Errorf("expected name %q, got %q", "token_stats", s.Name())
	}
	if s.Description() == "" {
		t.Error("description should not be empty")
	}

	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Fatalf("invalid input schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("expected schema type 'object', got %v", schema["type"])
	}
}

func TestTokenStatsSkill_AdminOnly(t *testing.T) {
	store := newTestStore(t)
	s := New(store)

	// No role set — should reject.
	ctx := context.Background()
	result, err := s.Execute(ctx, json.RawMessage(`{"period":"week"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "only available to administrators") {
		t.Errorf("expected admin-only rejection, got: %s", result)
	}

	// Regular user — should reject.
	ctx = skill.WithUserRole(context.Background(), "regular")
	result, err = s.Execute(ctx, json.RawMessage(`{"period":"week"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "only available to administrators") {
		t.Errorf("expected admin-only rejection for regular user, got: %s", result)
	}

	// Admin — should NOT reject.
	ctx = skill.WithUserRole(context.Background(), "admin")
	result, err = s.Execute(ctx, json.RawMessage(`{"period":"week"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "only available to administrators") {
		t.Errorf("admin should not be rejected, got: %s", result)
	}
}

func TestTokenStatsSkill_Execute(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Seed usage data.
	now := time.Now().Truncate(time.Hour)
	rows := []storage.UsageUpsert{
		{ChatID: "chat1", UserID: "u1", Model: "claude-sonnet", Provider: "anthropic", Hour: now, InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, Requests: 5, CostUSD: 0.05},
		{ChatID: "chat2", UserID: "u2", Model: "gpt-4o", Provider: "openai", Hour: now, InputTokens: 2000, OutputTokens: 1000, Requests: 10, CostUSD: 0.10},
		{ChatID: "chat1", UserID: "u1", Model: "claude-sonnet", Provider: "anthropic", Hour: now.Add(-24 * time.Hour), InputTokens: 500, OutputTokens: 250, CacheReadTokens: 50, Requests: 3, CostUSD: 0.03},
	}
	for _, r := range rows {
		if err := store.UpsertUsage(ctx, r); err != nil {
			t.Fatalf("inserting usage: %v", err)
		}
	}

	s := New(store)
	adminCtx := skill.WithUserRole(ctx, "admin")

	// Default period (week) — should include all data.
	result, err := s.Execute(adminCtx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify summary section.
	if !strings.Contains(result, "Token Usage Summary (last 7 days)") {
		t.Errorf("expected week summary header, got: %s", result)
	}
	if !strings.Contains(result, "**Input tokens**: 3500") {
		t.Errorf("expected total input 3500, got: %s", result)
	}
	if !strings.Contains(result, "**Output tokens**: 1750") {
		t.Errorf("expected total output 1750, got: %s", result)
	}
	if !strings.Contains(result, "**Total requests**: 18") {
		t.Errorf("expected total requests 18, got: %s", result)
	}

	// Verify model section.
	if !strings.Contains(result, "By Model") {
		t.Error("expected 'By Model' section")
	}
	if !strings.Contains(result, "claude-sonnet") {
		t.Error("expected claude-sonnet in model breakdown")
	}
	if !strings.Contains(result, "gpt-4o") {
		t.Error("expected gpt-4o in model breakdown")
	}

	// Verify daily section.
	if !strings.Contains(result, "Daily Breakdown") {
		t.Error("expected 'Daily Breakdown' section")
	}

	// Test with model filter.
	result, err = s.Execute(adminCtx, json.RawMessage(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "**Input tokens**: 2000") {
		t.Errorf("expected 2000 input tokens for gpt-4o filter, got: %s", result)
	}

	// Test "today" period.
	result, err = s.Execute(adminCtx, json.RawMessage(`{"period":"today"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Token Usage Summary (today)") {
		t.Errorf("expected today header, got: %s", result)
	}

	// Test "all" period.
	result, err = s.Execute(adminCtx, json.RawMessage(`{"period":"all"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Token Usage Summary (all time)") {
		t.Errorf("expected all header, got: %s", result)
	}
}

func TestTokenStatsSkill_NoData(t *testing.T) {
	store := newTestStore(t)
	s := New(store)

	adminCtx := skill.WithUserRole(context.Background(), "admin")
	result, err := s.Execute(adminCtx, json.RawMessage(`{"period":"week"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "No usage data available") {
		t.Errorf("expected no-data message, got: %s", result)
	}
}
