package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

func TestSaveSkillExecutionDefaults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Origin and CreatedAt left zero — should be defaulted.
	e := &domain.SkillExecution{
		ChatID:    "chat1",
		SkillName: "websearch",
		Success:   true,
	}
	if err := store.SaveSkillExecution(ctx, e); err != nil {
		t.Fatalf("save: %v", err)
	}

	stats, err := store.GetSkillStats(ctx, storage.SkillStatsFilter{})
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 skill stat, got %d", len(stats))
	}
	if stats[0].SkillName != "websearch" || stats[0].TotalCalls != 1 {
		t.Errorf("unexpected stat: %+v", stats[0])
	}
	if stats[0].LastUsed.IsZero() {
		t.Error("expected LastUsed to be defaulted, got zero")
	}
}

func TestGetSkillStatsAggregation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now()
	rows := []*domain.SkillExecution{
		{ChatID: "c1", UserID: "u1", SkillName: "websearch", Success: true, DurationMs: 100, Origin: domain.SkillOriginMain, CreatedAt: now.Add(-3 * time.Hour)},
		{ChatID: "c1", UserID: "u1", SkillName: "websearch", Success: false, DurationMs: 300, Origin: domain.SkillOriginMain, CreatedAt: now.Add(-2 * time.Hour)},
		{ChatID: "c1", UserID: "u1", SkillName: "websearch", Success: true, DurationMs: 200, Origin: domain.SkillOriginSubagent, CreatedAt: now.Add(-1 * time.Hour)},
		{ChatID: "c2", UserID: "u2", SkillName: "weather", Success: true, DurationMs: 50, Origin: domain.SkillOriginMain, CreatedAt: now},
	}
	for _, r := range rows {
		if err := store.SaveSkillExecution(ctx, r); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	// No filter — aggregate everything, ordered by call count DESC.
	stats, err := store.GetSkillStats(ctx, storage.SkillStatsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(stats))
	}
	ws := stats[0] // websearch has more calls
	if ws.SkillName != "websearch" {
		t.Fatalf("expected websearch first, got %s", ws.SkillName)
	}
	if ws.TotalCalls != 3 || ws.SuccessCalls != 2 || ws.FailureCalls != 1 {
		t.Errorf("websearch counts wrong: %+v", ws)
	}
	if ws.AvgDurationMs != 200 { // (100+300+200)/3
		t.Errorf("expected avg 200, got %f", ws.AvgDurationMs)
	}
	if ws.MaxDurationMs != 300 {
		t.Errorf("expected max 300, got %d", ws.MaxDurationMs)
	}

	// Filter by origin — only main-loop executions.
	stats, err = store.GetSkillStats(ctx, storage.SkillStatsFilter{Origin: domain.SkillOriginMain})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stats {
		if st.SkillName == "websearch" && st.TotalCalls != 2 {
			t.Errorf("expected 2 main-origin websearch calls, got %d", st.TotalCalls)
		}
	}

	// Filter by user.
	stats, err = store.GetSkillStats(ctx, storage.SkillStatsFilter{UserID: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].SkillName != "weather" {
		t.Errorf("expected only weather for u2, got %+v", stats)
	}

	// Filter by time range — exclude the oldest websearch row.
	stats, err = store.GetSkillStats(ctx, storage.SkillStatsFilter{From: now.Add(-2*time.Hour - time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range stats {
		if st.SkillName == "websearch" && st.TotalCalls != 2 {
			t.Errorf("expected 2 websearch calls in range, got %d", st.TotalCalls)
		}
	}

	// Empty result.
	stats, err = store.GetSkillStats(ctx, storage.SkillStatsFilter{UserID: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("expected no stats, got %d", len(stats))
	}
}
