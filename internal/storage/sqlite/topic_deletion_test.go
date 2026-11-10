package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
)

func deletionStore(t *testing.T) (*Store, domain.TelegramTopicDeletionScope) {
	t.Helper()
	s, e := New(filepath.Join(t.TempDir(), "deletion.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.EnsureDaySchema(ctx); e != nil {
		t.Fatal(e)
	}
	scope := domain.TelegramTopicDeletionScope{BotID: 777, ConversationID: "tgconv:delete", InstanceID: "bot-a", OwnerID: "alice", ChatID: 42, ThreadID: 100}
	for _, r := range []domain.TelegramConversationRoute{{BotID: 777, ConversationID: scope.ConversationID, InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: 42, ThreadID: 100, Private: true}, {ConversationID: "tgconv:neighbor", InstanceID: "bot-b", OwnerID: "bob", ChatID: 42, ThreadID: 100, Private: true}} {
		if e = s.PutTelegramRoute(ctx, &r); e != nil {
			t.Fatal(e)
		}
	}
	return s, scope
}
func TestTopicDeletionIsolationCancellationAndLateWriteFences(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	if e := s.CreateVectorTables(ctx); e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{scope.ConversationID, "tgconv:neighbor", "42"} {
		if e := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: key, UserID: "alice", Role: "user", Content: "secret"}); e != nil {
			t.Fatal(e)
		}
	}
	fact := &domain.Fact{ChatID: scope.ConversationID, UserID: "alice", Content: "explicit memory"}
	if e := s.SaveFact(ctx, fact); e != nil {
		t.Fatal(e)
	}
	insight := &domain.Insight{ChatID: scope.ConversationID, UserID: "alice", Content: "derived secret"}
	if e := s.SaveInsight(ctx, insight); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveInsightVector(ctx, insight.ID, []float32{0.1}); e != nil {
		t.Fatal(e)
	}
	if e := s.UpsertTechFact(ctx, &domain.TechFact{ChatID: scope.ConversationID, Category: "topic", Key: "one", Value: "derived"}); e != nil {
		t.Fatal(e)
	}
	rem := &domain.Reminder{ChatID: scope.ConversationID, UserID: "alice", Title: "secret reminder", DueAt: time.Now().Add(-time.Minute), Status: "pending"}
	if e := s.CreateReminder(ctx, rem); e != nil {
		t.Fatal(e)
	}
	job := &domain.AgentJob{DeliveryChatID: scope.ConversationID, UserID: "alice", Prompt: "secret job", Enabled: true}
	if e := s.CreateAgentJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	tasks := []*domain.Task{{Type: "reminder.fire", Payload: marshalDeletion(t, map[string]any{"reminder_id": rem.ID, "chat_id": scope.ConversationID})}, {Type: "agent.job", Payload: marshalDeletion(t, map[string]any{"job_id": job.ID, "user_id": "alice", "prompt": job.Prompt, "delivery_chat_id": scope.ConversationID})}, {Type: "techfact.analyze", Payload: marshalDeletion(t, map[string]any{"chat_id": scope.ConversationID})}, {Type: "unknown.global", Payload: marshalDeletion(t, map[string]any{"chat_id": scope.ConversationID})}}
	for _, task := range tasks {
		if e := s.CreateTask(ctx, task); e != nil {
			t.Fatal(e)
		}
	}
	// Include a running task and an uncertain reminder in the atomic cleanup.
	if _, e := s.db.NewUpdate().Model(tasks[0]).Set("status = 'running'").WherePK().Exec(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.NewUpdate().Model(rem).Set("status = 'delivery_unknown'").WherePK().Exec(ctx); e != nil {
		t.Fatal(e)
	}
	card := &dayview.Card{ID: "card", Owner: "alice", Instance: "bot-a", Chat: 42, Thread: 100, ItemsJSON: "secret", Expires: time.Now().Add(time.Hour)}
	if e := s.SaveDayCard(ctx, card); e != nil {
		t.Fatal(e)
	}
	d, e := s.BeginTelegramTopicDeletion(ctx, scope, "manual_sync")
	if e != nil {
		t.Fatal(e)
	}
	if d.Messages != 1 || d.Reminders != 1 || d.Jobs != 1 || d.Tasks != 3 {
		t.Fatalf("counts %+v", d)
	}
	for n, task := range tasks {
		got := new(domain.Task)
		if e = s.db.NewSelect().Model(got).Where("id = ?", task.ID).Scan(ctx); e != nil {
			t.Fatal(e)
		}
		if n < 3 && (got.Status != domain.TaskStatusCancelled || got.Payload != "{}") {
			t.Fatalf("not cancelled/scrubbed %+v", got)
		}
		if n == 3 && got.Status != domain.TaskStatusPending {
			t.Fatal("unknown global task cancelled")
		}
	}
	if e = s.FailTask(ctx, tasks[0].ID, "late failure"); e != nil && !errors.Is(e, domain.ErrConversationDeleted) {
		t.Fatal(e)
	}
	_ = s.CompleteTask(ctx, tasks[0].ID, "late secret")
	if runnable, checkErr := s.TaskStillRunnable(ctx, tasks[0].ID); checkErr != nil || runnable {
		t.Fatalf("cancelled runnable %v %v", runnable, checkErr)
	}
	late := make([]func() error, 0, 11)
	late = append(late,
		func() error {
			return s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, Content: "late", Role: "assistant"})
		},
		func() error {
			return s.ReplaceMessagesWithSummary(ctx, scope.ConversationID, 1, &domain.ChatMessage{ChatID: scope.ConversationID, Content: "late summary", Role: "system"})
		},
		func() error {
			return s.SaveFact(ctx, &domain.Fact{ChatID: scope.ConversationID, Content: "late memory"})
		},
		func() error {
			return s.UpsertTechFact(ctx, &domain.TechFact{ChatID: scope.ConversationID, Category: "topic", Key: "late"})
		},
		func() error {
			return s.CreateReminder(ctx, &domain.Reminder{ChatID: scope.ConversationID, Title: "late"})
		},
		func() error {
			return s.CreateAgentJob(ctx, &domain.AgentJob{DeliveryChatID: scope.ConversationID, Prompt: "late"})
		},
		func() error { return s.SaveDayCard(ctx, card) },
		func() error { return s.ClaimDayOpForDeletionTest(ctx, scope.ConversationID) },
		func() error {
			return s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: scope.ConversationID, Status: "open"})
		},
		func() error {
			return s.SaveInsight(ctx, &domain.Insight{ChatID: scope.ConversationID, Content: "late insight"})
		},
		func() error { return s.SaveFactVector(ctx, fact.ID, []float32{0.2}) },
	)
	if checkErr := s.SaveInsightVector(ctx, insight.ID, []float32{0.2}); checkErr == nil {
		t.Fatal("orphan insight embedding resurrected")
	}
	var vectorCount int
	if checkErr := s.db.NewRaw("SELECT COUNT(*) FROM insight_vectors WHERE insight_id = ?", insight.ID).Scan(ctx, &vectorCount); checkErr != nil || vectorCount != 0 {
		t.Fatal("deleted insight vector survived", vectorCount, checkErr)
	}
	for n, f := range late {
		if e = f(); !errors.Is(e, domain.ErrConversationDeleted) {
			t.Fatalf("late write %d allowed: %v", n, e)
		}
	}
	for _, key := range []string{"tgconv:neighbor", "42"} {
		h, checkErr := s.GetHistory(ctx, key, 0)
		if checkErr != nil || len(h) != 1 {
			t.Fatalf("neighbor %s damaged: %v %v", key, h, checkErr)
		}
	}

	gotFact := new(domain.Fact)
	if e = s.db.NewSelect().Model(gotFact).Where("id = ?", fact.ID).Scan(ctx); e != nil || gotFact.Content != "explicit memory" {
		t.Fatal("owner memory removed", e)
	}
	if n, e := s.CountTechFacts(ctx, scope.ConversationID); e != nil || n != 0 {
		t.Fatal("derived context survived", n, e)
	}
	if c, e := s.GetDayCard(ctx, "card"); e != nil || c.ItemsJSON != "" || c.Expires.After(time.Now()) {
		t.Fatal("card payload survived", c, e)
	}
}
func (s *Store) ClaimDayOpForDeletionTest(ctx context.Context, key string) error {
	_, e := s.ClaimDayOperation(ctx, &dayview.Operation{ID: "late-op", Conversation: key})
	return e
}
func marshalDeletion(t *testing.T, v any) string {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestTopicDeletionIntentScopeCrashRecoveryAndLegacyState(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	bad := scope
	bad.OwnerID = "mallory"
	if _, e := s.BeginTelegramTopicDeletion(ctx, bad, "manual_sync"); e == nil {
		t.Fatal("foreign deletion allowed")
	}
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "command"); e == nil {
		t.Fatal("unapproved command allowed")
	}
	if e := s.PutTelegramTopicState(ctx, &domain.TelegramTopicState{ConversationID: scope.ConversationID, Status: "deleted"}); e != nil {
		t.Fatal(e)
	}
	if unavailable, e := s.ConversationUnavailable(ctx, scope.ConversationID); e != nil || !unavailable {
		t.Fatal("legacy deletion ignored", unavailable, e)
	}
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "manual_sync"); e != nil {
		t.Fatal("legacy cleanup unavailable", e)
	}
	if _, e := s.FinalizeTelegramTopicDeletion(ctx, scope); e != nil {
		t.Fatal(e)
	}
	rows, e := s.ListTelegramTopicSyncRoutes(ctx, "bot-a", "", 10)
	if e != nil || len(rows) != 1 {
		t.Fatal("lost unclaimed notice recovery", rows, e)
	}
	if yes, checkErr := s.ClaimTelegramTopicDeletionNotice(ctx, scope); checkErr != nil || !yes {
		t.Fatal("notice claim", yes, checkErr)
	}
	if yes, checkErr := s.ClaimTelegramTopicDeletionNotice(ctx, scope); checkErr != nil || yes {
		t.Fatal("notice replay", yes, checkErr)
	}
	rows, e = s.ListTelegramTopicSyncRoutes(ctx, "bot-a", "", 10)
	if e != nil || len(rows) != 0 {
		t.Fatal("finalized notice retained", rows, e)
	}
	if unavailable, e := s.ConversationUnavailable(ctx, "ordinary"); e != nil || unavailable {
		t.Fatal("generic chat broken", e)
	}
}
func TestTopicDeletionConcurrentLateWritersCannotResurrect(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, Role: "assistant", Content: "racing"})
		}()
	}
	wg.Wait() // populated writers followed by deletion; late waves must remain fenced
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "manual_sync"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, Role: "assistant", Content: "late"}); !errors.Is(e, domain.ErrConversationDeleted) {
				t.Errorf("late write: %v", e)
			}
		}()
	}
	wg.Wait()
	if h, e := s.GetHistory(ctx, scope.ConversationID, 0); e != nil || len(h) != 0 {
		t.Fatal("resurrected history", h, e)
	}
}

func TestTopicDeletionCleanupFailureRollsBackTombstoneAndHistory(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	if e := s.SaveMessage(ctx, &domain.ChatMessage{ChatID: scope.ConversationID, Content: "preserve on failed cleanup", Role: "user"}); e != nil {
		t.Fatal(e)
	}
	// A corrupt/missing required data table must stop deletion atomically.
	if _, e := s.db.ExecContext(ctx, "DROP TABLE tech_facts"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "manual_sync"); e == nil {
		t.Fatal("partial cleanup reported success")
	}
	if d, e := s.GetTelegramTopicDeletion(ctx, scope.ConversationID); e != nil || d != nil {
		t.Fatal("failed transaction left tombstone", d, e)
	}
	if h, e := s.GetHistory(ctx, scope.ConversationID, 0); e != nil || len(h) != 1 {
		t.Fatal("failed deletion lost history", h, e)
	}
}
func TestTopicDeletionCommandIntentMustMatchInsideTransaction(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	op := &domain.TelegramTopicOperation{ID: "approved", Digest: "digest", Action: "delete", OwnerID: scope.OwnerID, InstanceID: scope.InstanceID, ChatID: scope.ChatID, ThreadID: scope.ThreadID}
	if _, claimed, e := s.ClaimTelegramTopicOperation(ctx, op); e != nil || !claimed {
		t.Fatal(claimed, e)
	}
	scope.OperationID = op.ID
	scope.Digest = op.Digest
	bad := scope
	bad.Digest = "other"
	if _, e := s.BeginTelegramTopicDeletion(ctx, bad, "command"); e == nil {
		t.Fatal("wrong digest authorized deletion")
	}
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "command"); e != nil {
		t.Fatal(e)
	}
	scope.OperationID = ""
	scope.Digest = ""
	if _, e := s.BeginTelegramTopicDeletion(ctx, scope, "command"); e != nil {
		t.Fatal("persisted tombstone cannot reconcile", e)
	}
}

func TestTopicDeletionPinsPersistedBotAcrossRestartAndRejectsLegacyGuess(t *testing.T) {
	s, scope := deletionStore(t)
	ctx := context.Background()
	wrong := scope
	wrong.BotID++
	if _, e := s.BeginTelegramTopicDeletion(ctx, wrong, "telegram_missing"); e == nil {
		t.Fatal("replacement bot erased old context")
	}
	route, e := s.GetTelegramRoute(ctx, scope.ConversationID)
	if e != nil {
		t.Fatal(e)
	}
	route.BotID++
	if e = s.PutTelegramRoute(ctx, route); e == nil {
		t.Fatal("replacement token rebound persisted bot")
	}
	legacy := &domain.TelegramConversationRoute{ConversationID: "tgconv:legacy", InstanceID: "bot-a", OwnerID: "alice", ChatID: 42, ThreadID: 99, Private: true}
	if e = s.PutTelegramRoute(ctx, legacy); e != nil {
		t.Fatal(e)
	}
	legacy.BotID = 777
	if e = s.PutTelegramRoute(ctx, legacy); e == nil {
		t.Fatal("legacy identity guessed from current token")
	}
	rows, e := s.ListTelegramTopicSyncRoutes(ctx, "bot-a", "", 10)
	if e != nil || len(rows) != 1 || rows[0].ConversationID != scope.ConversationID {
		t.Fatal("unproven legacy route destructively scanned", rows, e)
	}
	// Simulate the schema that predates durable identity. The migration must add
	// an unknown zero, never bind old data to whichever bot starts next.
	old, e := New(filepath.Join(t.TempDir(), "legacy.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if _, e = old.db.ExecContext(ctx, `CREATE TABLE telegram_conversation_routes(conversation_id TEXT PRIMARY KEY,instance_id TEXT,owner_id TEXT,chat_id BIGINT,thread_id INTEGER,private BOOLEAN,updated_at TIMESTAMP)`); e != nil {
		t.Fatal(e)
	}
	if _, e = old.db.ExecContext(ctx, `INSERT INTO telegram_conversation_routes VALUES('legacy','bot-a','alice',42,99,TRUE,CURRENT_TIMESTAMP)`); e != nil {
		t.Fatal(e)
	}
	if e = old.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	got, e := old.GetTelegramRoute(ctx, "legacy")
	if e != nil || got.BotID != 0 {
		t.Fatal("migration inferred a bot identity", got, e)
	}
}
