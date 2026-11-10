package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type deletionHandler struct {
	started       chan struct{}
	cancelled     chan struct{}
	release       chan struct{}
	uncooperative bool
}

func (*deletionHandler) Type() string { return "techfact.analyze" }
func (h *deletionHandler) Handle(ctx context.Context, _ string) (string, error) {
	close(h.started)
	if h.uncooperative {
		<-h.release
		return `{"late":"secret"}`, nil
	}
	<-ctx.Done()
	close(h.cancelled)
	return "", ctx.Err()
}
func TestWorkerTopicDeletionCancelsLiveAndDiscardsLateResults(t *testing.T) {
	for _, uncooperative := range []bool{false, true} {
		t.Run(map[bool]string{false: "cooperative", true: "late-provider"}[uncooperative], func(t *testing.T) {
			ctx := context.Background()
			s, e := sqlite.New(filepath.Join(t.TempDir(), "worker.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.RunMigrations(ctx); e != nil {
				t.Fatal(e)
			}
			scope := domain.TelegramTopicDeletionScope{BotID: 777, ConversationID: "tgconv:worker", InstanceID: "bot", OwnerID: "alice", ChatID: 42, ThreadID: 9}
			if e = s.PutTelegramRoute(ctx, &domain.TelegramConversationRoute{BotID: 777, ConversationID: scope.ConversationID, InstanceID: scope.InstanceID, OwnerID: scope.OwnerID, ChatID: scope.ChatID, ThreadID: scope.ThreadID, Private: true}); e != nil {
				t.Fatal(e)
			}
			task := &domain.Task{Type: "techfact.analyze", Payload: `{"chat_id":"tgconv:worker"}`}
			if e = s.CreateTask(ctx, task); e != nil {
				t.Fatal(e)
			}
			w := NewWorker(s, WorkerConfig{}, zap.NewNop())
			h := &deletionHandler{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), uncooperative: uncooperative}
			w.Register(h)
			claimed, e := s.ClaimTask(ctx, w.id, nil)
			if e != nil || claimed == nil {
				t.Fatal("claim", e)
			}
			done := make(chan struct{})
			go func() { defer close(done); w.executeTask(ctx, claimed) }()
			select {
			case <-h.started:
			case <-time.After(2 * time.Second):
				t.Fatal("handler not started")
			}
			if _, e = s.BeginTelegramTopicDeletion(ctx, scope, "manual_sync"); e != nil {
				t.Fatal(e)
			}
			if uncooperative {
				close(h.release)
			} else {
				select {
				case <-h.cancelled:
				case <-time.After(2 * time.Second):
					t.Fatal("running handler not cancelled")
				}
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not finish")
			}
			tasks, e := s.ListTasks(ctx, storage.TaskFilter{})
			if e != nil || len(tasks) != 1 {
				t.Fatal(tasks, e)
			}
			if tasks[0].Status != domain.TaskStatusCancelled || tasks[0].Result != "" || tasks[0].Payload != "{}" {
				t.Fatalf("late result resurrected: %+v", tasks[0])
			}
		})
	}
}
