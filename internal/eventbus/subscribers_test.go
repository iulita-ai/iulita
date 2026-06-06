package eventbus

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return store
}

func TestSkillTelemetrySubscriberPersists(t *testing.T) {
	store := newTestStore(t)
	bus := New(zap.NewNop())
	RegisterSkillTelemetrySubscriber(bus, store, zap.NewNop())

	ctx := context.Background()
	bus.Publish(ctx, Event{
		Type: SkillExecuted,
		Payload: SkillExecutedPayload{
			ChatID:     "chat1",
			UserID:     "user1",
			SkillName:  "websearch",
			ToolCallID: "tc1",
			Success:    true,
			DurationMs: 123,
			Iteration:  2,
			Origin:     domain.SkillOriginMain,
		},
	})
	// Origin left empty should default to "main" on persist.
	bus.Publish(ctx, Event{
		Type: SkillExecuted,
		Payload: SkillExecutedPayload{
			ChatID:    "chat1",
			UserID:    "user1",
			SkillName: "weather",
			Success:   false,
		},
	})
	bus.Shutdown() // wait for async handlers

	stats, err := store.GetSkillStats(ctx, storage.SkillStatsFilter{})
	if err != nil {
		t.Fatalf("get stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 skills persisted, got %d", len(stats))
	}

	byName := map[string]storage.SkillStat{}
	for _, st := range stats {
		byName[st.SkillName] = st
	}
	if ws, ok := byName["websearch"]; !ok || ws.SuccessCalls != 1 || ws.AvgDurationMs != 123 {
		t.Errorf("websearch stat wrong: %+v", ws)
	}
	if wx, ok := byName["weather"]; !ok || wx.FailureCalls != 1 {
		t.Errorf("weather stat wrong: %+v", wx)
	}

	// The default-origin row must have been stored as "main".
	mainStats, err := store.GetSkillStats(ctx, storage.SkillStatsFilter{Origin: domain.SkillOriginMain})
	if err != nil {
		t.Fatal(err)
	}
	if len(mainStats) != 2 {
		t.Errorf("expected both rows under main origin, got %d", len(mainStats))
	}
}
