package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestKnownScheduledTaskRejectsMalformedAuthorityButGlobalPayloadRemainsOpaque(t *testing.T) {
	s, _ := deletionStore(t)
	ctx := context.Background()
	if err := s.CreateTask(ctx, &domain.Task{Type: "heartbeat.check", Payload: "not-json"}); err == nil {
		t.Fatal("known conversation task accepted malformed authority")
	}
	if err := s.CreateTask(ctx, &domain.Task{Type: "global.opaque", Payload: "not-json"}); err != nil {
		t.Fatal("unknown global task payload was interpreted", err)
	}
	task := &domain.Task{Type: "heartbeat.check", Payload: `{"chat_id":"42"}`, Status: domain.TaskStatusRunning}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.NewUpdate().Model((*domain.Task)(nil)).Set("payload = ?", "not-json").Where("id = ?", task.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if runnable, err := s.TaskStillRunnable(ctx, task.ID); err != nil || runnable {
		t.Fatal("corrupt active authority remained runnable", runnable, err)
	}
	stored := new(domain.Task)
	if err := s.db.NewSelect().Model(stored).Where("id = ?", task.ID).Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.TaskStatusCancelled || stored.Payload != "{}" {
		t.Fatalf("corrupt authority was not terminally cancelled: %+v", stored)
	}
}

func TestAbsentAgentJobPayloadRemainsMetadataButCannotRun(t *testing.T) {
	s, _ := deletionStore(t)
	ctx := context.Background()
	for _, raw := range []string{"", "   "} {
		task := &domain.Task{Type: "agent.job", Payload: raw, Status: domain.TaskStatusRunning}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal("legacy metadata-only receipt creation failed", err)
		}
		if runnable, err := s.TaskStillRunnable(ctx, task.ID); err != nil || runnable {
			t.Fatal("metadata receipt acquired execution authority", runnable, err)
		}
		stored := new(domain.Task)
		if err := s.db.NewSelect().Model(stored).Where("id = ?", task.ID).Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if stored.Status != domain.TaskStatusCancelled || stored.Payload != "{}" {
			t.Fatalf("metadata-only receipt was not terminally cancelled before dispatch: %+v", stored)
		}
	}
	if err := s.CreateTask(ctx, &domain.Task{Type: "agent.job", Payload: "malformed nonempty"}); err == nil {
		t.Fatal("nonempty malformed authority accepted")
	}
}
