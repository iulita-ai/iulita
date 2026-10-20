package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
)

// SSH firing compaction relies on task IDs never being reused after cleanup.
func TestTaskIDsAreMonotonicAfterDeletion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var ddl string
	if e := store.db.NewRaw("SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(ctx, &ddl); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT") {
		t.Fatalf("task IDs can be reused: %s", ddl)
	}
	first := &domain.Task{Type: "agent.job", ScheduledAt: time.Now()}
	if e := store.CreateTask(ctx, first); e != nil {
		t.Fatal(e)
	}
	if _, e := store.db.NewDelete().Model((*domain.Task)(nil)).Where("id = ?", first.ID).Exec(ctx); e != nil {
		t.Fatal(e)
	}
	next := &domain.Task{Type: "agent.job", ScheduledAt: time.Now()}
	if e := store.CreateTask(ctx, next); e != nil {
		t.Fatal(e)
	}
	if next.ID <= first.ID {
		t.Fatalf("task ID reused: %d then%d", first.ID, next.ID)
	}
}
