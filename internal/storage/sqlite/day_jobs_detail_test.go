package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestDayJobDetailsAndArtifactOwnerScope(t *testing.T) {
	ctx := context.Background()
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	job := &domain.AgentJob{UserID: "alice", Name: "Research", DeliveryChatID: "topic-a", Interval: "24h", Enabled: true}
	if e = s.CreateAgentJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	started := time.Now().Add(-time.Minute)
	finished := time.Now()
	task := &domain.Task{Type: "agent.job", UniqueKey: fmt.Sprintf("agent.job:%d", job.ID), Status: domain.TaskStatusDone, Result: "Owned report artifact", StartedAt: &started, FinishedAt: &finished, ScheduledAt: started}
	if e = s.CreateTask(ctx, task); e != nil {
		t.Fatal(e)
	}
	source := DayJobs{Store: s, Chat: "topic-a"}
	items, e := source.List(ctx, "alice", time.Now(), time.UTC)
	if e != nil || len(items) != 1 {
		t.Fatalf("items %+v %v", items, e)
	}
	detail, e := source.Detail(ctx, "alice", items[0])
	if e != nil || detail.Result != task.Result || detail.StartedAt == nil || detail.FinishedAt == nil {
		t.Fatalf("detail %+v %v", detail, e)
	}
	if _, e = source.Detail(ctx, "bob", items[0]); e == nil {
		t.Fatal("foreign job artifact exposed")
	}
	wrong := DayJobs{Store: s, Chat: "topic-b"}
	if _, e = wrong.Detail(ctx, "alice", items[0]); e == nil {
		t.Fatal("wrong conversation exposed artifact")
	}
	items[0].Revision = "stale"
	if _, e = source.Detail(ctx, "alice", items[0]); e == nil {
		t.Fatal("stale job detail accepted")
	}
}
