package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/storage"
)

func TestUpsertUsage(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	hour := time.Now().Truncate(time.Hour)

	// Insert a new row.
	rec := storage.UsageUpsert{
		ChatID:              "chat1",
		UserID:              "user1",
		Model:               "claude-sonnet-4-5-20250929",
		Provider:            "anthropic",
		Hour:                hour,
		InputTokens:         100,
		OutputTokens:        50,
		CacheReadTokens:     10,
		CacheCreationTokens: 5,
		Requests:            1,
		CostUSD:             0.01,
	}
	if err := store.UpsertUsage(ctx, rec); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Verify via summary.
	summary, err := store.GetUsageSummary(ctx, storage.UsageFilter{ChatID: "chat1"})
	if err != nil {
		t.Fatalf("get summary after first upsert: %v", err)
	}
	if summary.TotalInputTokens != 100 {
		t.Errorf("expected input tokens 100, got %d", summary.TotalInputTokens)
	}
	if summary.TotalOutputTokens != 50 {
		t.Errorf("expected output tokens 50, got %d", summary.TotalOutputTokens)
	}
	if summary.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", summary.TotalRequests)
	}

	// Upsert same chat_id + model + hour — should accumulate.
	rec2 := storage.UsageUpsert{
		ChatID:              "chat1",
		UserID:              "user1",
		Model:               "claude-sonnet-4-5-20250929",
		Provider:            "anthropic",
		Hour:                hour,
		InputTokens:         200,
		OutputTokens:        100,
		CacheReadTokens:     20,
		CacheCreationTokens: 10,
		Requests:            2,
		CostUSD:             0.02,
	}
	if upsertErr := store.UpsertUsage(ctx, rec2); upsertErr != nil {
		t.Fatalf("second upsert: %v", upsertErr)
	}

	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{ChatID: "chat1"})
	if err != nil {
		t.Fatalf("get summary after second upsert: %v", err)
	}
	if summary.TotalInputTokens != 300 {
		t.Errorf("expected accumulated input tokens 300, got %d", summary.TotalInputTokens)
	}
	if summary.TotalOutputTokens != 150 {
		t.Errorf("expected accumulated output tokens 150, got %d", summary.TotalOutputTokens)
	}
	if summary.TotalCacheReadTokens != 30 {
		t.Errorf("expected accumulated cache read tokens 30, got %d", summary.TotalCacheReadTokens)
	}
	if summary.TotalCacheCreationTokens != 15 {
		t.Errorf("expected accumulated cache creation tokens 15, got %d", summary.TotalCacheCreationTokens)
	}
	if summary.TotalRequests != 3 {
		t.Errorf("expected accumulated requests 3, got %d", summary.TotalRequests)
	}
	if summary.TotalCostUSD < 0.029 || summary.TotalCostUSD > 0.031 {
		t.Errorf("expected accumulated cost ~0.03, got %f", summary.TotalCostUSD)
	}

	// Different model in the same hour should NOT merge.
	rec3 := storage.UsageUpsert{
		ChatID:       "chat1",
		UserID:       "user1",
		Model:        "ollama-llama3",
		Provider:     "ollama",
		Hour:         hour,
		InputTokens:  50,
		OutputTokens: 25,
		Requests:     1,
		CostUSD:      0,
	}
	if upsertErr := store.UpsertUsage(ctx, rec3); upsertErr != nil {
		t.Fatalf("third upsert (different model): %v", upsertErr)
	}

	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{ChatID: "chat1"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 350 {
		t.Errorf("expected total input 350 across models, got %d", summary.TotalInputTokens)
	}
	if summary.TotalRequests != 4 {
		t.Errorf("expected total requests 4 across models, got %d", summary.TotalRequests)
	}
}

func TestGetUsageSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	twoDaysAgo := now.Add(-48 * time.Hour)

	// Insert rows across different times and models.
	rows := []storage.UsageUpsert{
		{ChatID: "chat1", UserID: "user1", Model: "claude", Provider: "anthropic", Hour: now, InputTokens: 100, OutputTokens: 50, Requests: 1, CostUSD: 0.01},
		{ChatID: "chat1", UserID: "user1", Model: "claude", Provider: "anthropic", Hour: yesterday, InputTokens: 200, OutputTokens: 100, Requests: 2, CostUSD: 0.02},
		{ChatID: "chat2", UserID: "user2", Model: "gpt4", Provider: "openai", Hour: now, InputTokens: 300, OutputTokens: 150, Requests: 3, CostUSD: 0.05},
		{ChatID: "chat1", UserID: "user1", Model: "claude", Provider: "anthropic", Hour: twoDaysAgo, InputTokens: 400, OutputTokens: 200, Requests: 4, CostUSD: 0.04},
	}
	for _, r := range rows {
		if err := store.UpsertUsage(ctx, r); err != nil {
			t.Fatalf("inserting row: %v", err)
		}
	}

	// No filter — should sum everything.
	summary, err := store.GetUsageSummary(ctx, storage.UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 1000 {
		t.Errorf("expected total input 1000, got %d", summary.TotalInputTokens)
	}
	if summary.TotalRequests != 10 {
		t.Errorf("expected total requests 10, got %d", summary.TotalRequests)
	}

	// Filter by date range (yesterday to now, exclusive of twoDaysAgo).
	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{
		From: yesterday,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should include yesterday(200+100) + now(100+50 + 300+150) = 600 input, 300 output
	if summary.TotalInputTokens != 600 {
		t.Errorf("expected 600 input tokens from yesterday onward, got %d", summary.TotalInputTokens)
	}

	// Filter by model.
	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{Model: "gpt4"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens for gpt4, got %d", summary.TotalInputTokens)
	}
	if summary.TotalRequests != 3 {
		t.Errorf("expected 3 requests for gpt4, got %d", summary.TotalRequests)
	}

	// Filter by chat_id.
	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{ChatID: "chat2"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 300 {
		t.Errorf("expected 300 input tokens for chat2, got %d", summary.TotalInputTokens)
	}

	// Filter by user_id.
	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{UserID: "user1"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 700 {
		t.Errorf("expected 700 input tokens for user1, got %d", summary.TotalInputTokens)
	}

	// Empty result — no matching data.
	summary, err = store.GetUsageSummary(ctx, storage.UsageFilter{Model: "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalInputTokens != 0 {
		t.Errorf("expected 0 for nonexistent model, got %d", summary.TotalInputTokens)
	}
}

func TestGetUsageByDay(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insert rows across 3 different days.
	day1 := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 16, 14, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 3, 17, 8, 0, 0, 0, time.UTC)

	rows := []storage.UsageUpsert{
		{ChatID: "c1", Model: "claude", Provider: "anthropic", Hour: day1, InputTokens: 100, OutputTokens: 50, Requests: 1, CostUSD: 0.01},
		{ChatID: "c1", Model: "claude", Provider: "anthropic", Hour: day1.Add(time.Hour), InputTokens: 100, OutputTokens: 50, Requests: 1, CostUSD: 0.01},
		{ChatID: "c1", Model: "claude", Provider: "anthropic", Hour: day2, InputTokens: 200, OutputTokens: 100, Requests: 2, CostUSD: 0.02},
		{ChatID: "c1", Model: "gpt4", Provider: "openai", Hour: day3, InputTokens: 300, OutputTokens: 150, Requests: 3, CostUSD: 0.05},
	}
	for _, r := range rows {
		if err := store.UpsertUsage(ctx, r); err != nil {
			t.Fatalf("inserting row: %v", err)
		}
	}

	// Get all days.
	daily, err := store.GetUsageByDay(ctx, storage.UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 3 {
		t.Fatalf("expected 3 days, got %d", len(daily))
	}

	// Results are ordered DESC, so day3 first.
	if daily[0].Date != "2026-03-17" {
		t.Errorf("expected first date 2026-03-17, got %s", daily[0].Date)
	}
	if daily[0].InputTokens != 300 {
		t.Errorf("expected 300 input tokens on day3, got %d", daily[0].InputTokens)
	}

	// Day1 should have two rows merged: 100+100 = 200 input tokens.
	if daily[2].Date != "2026-03-15" {
		t.Errorf("expected last date 2026-03-15, got %s", daily[2].Date)
	}
	if daily[2].InputTokens != 200 {
		t.Errorf("expected 200 input tokens on day1 (two hourly rows), got %d", daily[2].InputTokens)
	}
	if daily[2].Requests != 2 {
		t.Errorf("expected 2 requests on day1, got %d", daily[2].Requests)
	}

	// Filter by date range.
	daily, err = store.GetUsageByDay(ctx, storage.UsageFilter{
		From: day2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 2 {
		t.Fatalf("expected 2 days from day2 onward, got %d", len(daily))
	}
}

func TestGetUsageByModel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	hour := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC)

	rows := []storage.UsageUpsert{
		{ChatID: "c1", Model: "claude-sonnet", Provider: "anthropic", Hour: hour, InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, Requests: 1, CostUSD: 0.01},
		{ChatID: "c2", Model: "claude-sonnet", Provider: "anthropic", Hour: hour, InputTokens: 200, OutputTokens: 100, CacheReadTokens: 20, Requests: 2, CostUSD: 0.02},
		{ChatID: "c1", Model: "gpt-4o", Provider: "openai", Hour: hour, InputTokens: 300, OutputTokens: 150, Requests: 3, CostUSD: 0.05},
		{ChatID: "c1", Model: "llama3", Provider: "ollama", Hour: hour, InputTokens: 500, OutputTokens: 250, Requests: 5, CostUSD: 0},
	}
	for _, r := range rows {
		if err := store.UpsertUsage(ctx, r); err != nil {
			t.Fatalf("inserting row: %v", err)
		}
	}

	models, err := store.GetUsageByModel(ctx, storage.UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	// Ordered by total tokens DESC: llama3 (750) > claude-sonnet (300+150=450 total across chats) > gpt-4o (450).
	// Actually the ORDER BY is SUM(input_tokens) + SUM(output_tokens):
	// llama3: 500+250=750, claude-sonnet: (100+200)+(50+100)=450, gpt-4o: 300+150=450
	if models[0].Model != "llama3" {
		t.Errorf("expected llama3 first (most tokens), got %s", models[0].Model)
	}
	if models[0].Provider != "ollama" {
		t.Errorf("expected ollama provider, got %s", models[0].Provider)
	}
	if models[0].InputTokens != 500 {
		t.Errorf("expected 500 input tokens for llama3, got %d", models[0].InputTokens)
	}

	// Find claude-sonnet in results and verify aggregation across chats.
	var claudeFound bool
	for _, m := range models {
		if m.Model != "claude-sonnet" {
			continue
		}
		claudeFound = true
		if m.InputTokens != 300 {
			t.Errorf("expected 300 input tokens for claude-sonnet (100+200), got %d", m.InputTokens)
		}
		if m.OutputTokens != 150 {
			t.Errorf("expected 150 output tokens for claude-sonnet (50+100), got %d", m.OutputTokens)
		}
		if m.CacheReadTokens != 30 {
			t.Errorf("expected 30 cache read tokens for claude-sonnet (10+20), got %d", m.CacheReadTokens)
		}
		if m.Requests != 3 {
			t.Errorf("expected 3 requests for claude-sonnet (1+2), got %d", m.Requests)
		}
	}
	if !claudeFound {
		t.Error("claude-sonnet not found in model usage results")
	}

	// Filter by provider.
	models, err = store.GetUsageByModel(ctx, storage.UsageFilter{Provider: "anthropic"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model for anthropic provider, got %d", len(models))
	}
	if models[0].Model != "claude-sonnet" {
		t.Errorf("expected claude-sonnet, got %s", models[0].Model)
	}
}
