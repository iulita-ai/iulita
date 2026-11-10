package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
	"github.com/iulita-ai/iulita/internal/storage"
)

// This wrapper controls lifecycle independently of request cancellation. The
// underlying repository deliberately remains writable to catch assistant leaks;
// separate SQLite tests verify the durable transaction fences.
type deletionRepository struct {
	storage.Repository
	mu      sync.Mutex
	deleted map[string]bool
	fault   error
	reads   int
}

func (s *deletionRepository) ConversationUnavailable(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleted[key], s.fault
}
func (s *deletionRepository) GetHistory(ctx context.Context, key string, n int) ([]domain.ChatMessage, error) {
	s.mu.Lock()
	s.reads++
	s.mu.Unlock()
	return s.Repository.GetHistory(ctx, key, n)
}
func (s *deletionRepository) ReplaceMessagesWithSummary(ctx context.Context, key string, id int64, m *domain.ChatMessage) error {
	return s.Repository.(interface {
		ReplaceMessagesWithSummary(context.Context, string, int64, *domain.ChatMessage) error
	}).ReplaceMessagesWithSummary(ctx, key, id, m)
}
func (s *deletionRepository) remove(t *testing.T, key string) {
	t.Helper()
	s.mu.Lock()
	s.deleted[key] = true
	s.mu.Unlock()
	if err := s.ClearHistory(context.Background(), key); err != nil {
		t.Fatal(err)
	}
}
func deletionAssistant(t *testing.T, p llm.Provider) (*Assistant, *deletionRepository) {
	t.Helper()
	a := newTestAssistant(t, p)
	s := &deletionRepository{Repository: a.store, deleted: map[string]bool{}}
	a.store = s
	a.techAnalyzer = NewTechFactAnalyzer(s, a.logger)
	t.Cleanup(a.Shutdown)
	return a, s
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle action did not complete")
	}
}
func waitDeletedResult(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		if !errors.Is(err, domain.ErrConversationDeleted) {
			t.Fatalf("wanted deleted, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deleted conversation remained active")
	}
}

func TestDeletionDiscardsUncooperativeModelAndDoesNotBlockNeighbor(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	contexts := make(chan context.Context, 1)
	var calls atomic.Int64
	p := &funcProvider{fn: func(ctx context.Context, r llm.Request) (llm.Response, error) {
		calls.Add(1)
		if r.ChatID == "tgconv:deleted" {
			contexts <- ctx
			close(started)
			<-release
			return llm.Response{Content: "late private output", ToolCalls: []llm.ToolCall{{ID: "late", Name: "mutate", Input: json.RawMessage(`{}`)}}}, nil
		}
		return llm.Response{Content: "neighbor answer"}, nil
	}}
	a, s := deletionAssistant(t, p)
	danger := &deletionTestSkill{name: "mutate"}
	a.registry.Register(danger)
	result := make(chan error, 1)
	go func() {
		out, err := a.HandleMessage(context.Background(), topicMsg("tgconv:deleted", "initial"))
		if out != "" {
			result <- errors.New("late model output returned")
			return
		}
		result <- err
	}()
	waitSignal(t, started)
	active := <-contexts
	s.remove(t, "tgconv:deleted")
	// This must return without waiting for the provider that ignores cancellation.
	if err := a.CancelConversation(context.Background(), "tgconv:deleted"); err != nil {
		t.Fatal(err)
	}
	waitSignal(t, active.Done())
	if _, err := a.HandleMessage(context.Background(), topicMsg("tgconv:neighbor", "continue")); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitDeletedResult(t, result)
	h, _ := s.Repository.GetHistory(context.Background(), "tgconv:deleted", 0)
	if len(h) != 0 || danger.calls.Load() != 0 || calls.Load() != 2 {
		t.Fatal("late context, tool, or provider invocation", len(h), danger.calls.Load(), calls.Load())
	}
}

func TestDeletionCancelsBothManualCompressionAPIsBeforeLateSummary(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "compact", true: "console-compress"}[legacy], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			p := &funcProvider{fn: func(_ context.Context, r llm.Request) (llm.Response, error) {
				once.Do(func() { close(started) })
				<-release
				return llm.Response{Content: "late summary", FinishReason: "stop"}, nil
			}}
			a, s := deletionAssistant(t, p)
			seedTopic(t, a, "tgconv:summary-delete", 6, strings.Repeat("prior context ", 100))
			n := &compressionNotifier{}
			a.SetStatusNotifier(n)
			result := make(chan error, 1)
			go func() {
				if legacy {
					_, err := a.CompressNow(context.Background(), "tgconv:summary-delete")
					result <- err
				} else {
					_, err := a.CompactContext(context.Background(), "tgconv:summary-delete")
					result <- err
				}
			}()
			waitSignal(t, started)
			s.remove(t, "tgconv:summary-delete")
			_ = a.CancelConversation(context.Background(), "tgconv:summary-delete")
			close(release)
			waitDeletedResult(t, result)
			h, _ := s.Repository.GetHistory(context.Background(), "tgconv:summary-delete", 0)
			if len(h) != 0 || len(n.all()) != 0 {
				t.Fatal("late summary recreated context or success notice")
			}
		})
	}
}

func TestDeletionMonitorDetectsOtherProcessTombstone(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	p := &funcProvider{fn: func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return llm.Response{}, ctx.Err()
	}}
	a, s := deletionAssistant(t, p)
	result := make(chan error, 1)
	go func() {
		_, err := a.HandleMessage(context.Background(), topicMsg("tgconv:remote-delete", "message"))
		result <- err
	}()
	waitSignal(t, started)
	s.remove(t, "tgconv:remote-delete") // deliberately no local CancelConversation hook
	waitSignal(t, cancelled)
	waitDeletedResult(t, result)
}

func TestLifecycleReadFailureAndDeletedAdmissionFailClosed(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "database-error", true: "deleted"}[deleted], func(t *testing.T) {
			var calls atomic.Int64
			p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) { calls.Add(1); return llm.Response{}, nil }}
			a, s := deletionAssistant(t, p)
			fault := errors.New("lifecycle database unavailable")
			s.deleted["tgconv:deny"] = deleted
			if !deleted {
				s.fault = fault
			}
			if _, err := a.HandleMessage(context.Background(), topicMsg("tgconv:deny", "read private history")); err == nil {
				t.Fatal("unavailable admission passed")
			}
			if _, err := a.CompactContext(context.Background(), "tgconv:deny"); err == nil {
				t.Fatal("manual compact passed")
			}
			if err := a.ClearConversation(context.Background(), "tgconv:deny"); err == nil {
				t.Fatal("clear passed")
			}
			if calls.Load() != 0 || s.reads != 0 {
				t.Fatal("provider/history reached before lifecycle guard")
			}
		})
	}
}

type deletionTestSkill struct {
	name  string
	calls atomic.Int64
	wait  bool
}

func (s *deletionTestSkill) Name() string               { return s.name }
func (*deletionTestSkill) Description() string          { return "deletion lifecycle fixture" }
func (*deletionTestSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *deletionTestSkill) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	s.calls.Add(1)
	if s.wait {
		extended, cancel := skill.DeadlineExtenderFrom(ctx)(ctx, time.Hour)
		defer cancel()
		return interact.PrompterFrom(extended).Ask(extended, "confirm?", nil)
	}
	return "executed", nil
}
func (*deletionTestSkill) RequestTimeout() time.Duration { return time.Hour }

type deletionPromptFactory struct {
	started chan struct{}
	ctx     chan context.Context
}

func (f *deletionPromptFactory) PrompterFor(string) interact.PromptAsker { return f }
func (f *deletionPromptFactory) PrompterForActor(context.Context, string, interact.Actor) interact.PromptAsker {
	return f
}
func (*deletionPromptFactory) ConfirmerForActor(context.Context, string, interact.Actor) interact.ConfirmationAsker {
	return nil
}
func (f *deletionPromptFactory) Ask(ctx context.Context, _ string, _ []interact.Option) (string, error) {
	f.ctx <- ctx
	close(f.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestDeletionSurvivesDeadlineExtensionAndCancelsInteractivePrompt(t *testing.T) {
	var providers atomic.Int64
	p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		providers.Add(1)
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "ask", Name: "wait", Input: json.RawMessage(`{}`)}, {ID: "later", Name: "mutate", Input: json.RawMessage(`{}`)}}}, nil
	}}
	a, s := deletionAssistant(t, p)
	waiter := &deletionTestSkill{name: "wait", wait: true}
	danger := &deletionTestSkill{name: "mutate"}
	a.registry.Register(waiter)
	a.registry.Register(danger)
	f := &deletionPromptFactory{started: make(chan struct{}), ctx: make(chan context.Context, 1)}
	a.SetPrompterFactory(f)
	root, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	a.lifecycleRoot = root
	done := make(chan error, 1)
	go func() {
		_, err := a.HandleMessage(context.Background(), topicMsg("tgconv:prompt-delete", "need input"))
		done <- err
	}()
	waitSignal(t, f.started)
	promptCtx := <-f.ctx
	s.remove(t, "tgconv:prompt-delete")
	a.approvals.set("tgconv:prompt-delete", llm.ToolCall{ID: "legacy", Name: "mutate"}, skill.ApprovalPrompt)
	_ = a.CancelConversation(context.Background(), "tgconv:prompt-delete")
	waitSignal(t, promptCtx.Done())
	waitDeletedResult(t, done)
	if _, ok := a.approvals.take("tgconv:prompt-delete"); ok {
		t.Fatal("pending legacy approval retained")
	}
	if danger.calls.Load() != 0 || waiter.calls.Load() != 1 || providers.Load() != 1 {
		t.Fatal("post-deletion tool or synthesis executed")
	}
}

func TestLifecycleMonitorDatabaseErrorCancelsActiveWork(t *testing.T) {
	started := make(chan struct{})
	fault := errors.New("lifecycle read unavailable")
	p := &funcProvider{fn: func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		close(started)
		<-ctx.Done()
		return llm.Response{Content: "untrusted late response"}, nil
	}}
	a, s := deletionAssistant(t, p)
	done := make(chan error, 1)
	go func() {
		_, err := a.HandleMessage(context.Background(), topicMsg("tgconv:monitor-failure", "message"))
		done <- err
	}()
	waitSignal(t, started)
	s.mu.Lock()
	s.fault = fault
	s.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, fault) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("database lifecycle failure left provider active")
	}
}

func TestDeletionCancelsQueuedConversationBeforeHeldLaneReleases(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		calls.Add(1)
		close(started)
		<-release
		return llm.Response{Content: "late"}, nil
	}}
	a, s := deletionAssistant(t, p)
	first, queued := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := a.HandleMessage(context.Background(), topicMsg("tgconv:queued-delete", "first"))
		first <- err
	}()
	waitSignal(t, started)
	go func() { _, err := a.CompactContext(context.Background(), "tgconv:queued-delete"); queued <- err }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	registered := false
	for !registered {
		select {
		case <-timer.C:
			t.Fatal("queued operation did not register")
		case <-ticker.C:
			a.activeConversationMu.Lock()
			registered = len(a.activeConversations["tgconv:queued-delete"]) == 2
			a.activeConversationMu.Unlock()
		}
	}
	s.remove(t, "tgconv:queued-delete")
	_ = a.CancelConversation(context.Background(), "tgconv:queued-delete")
	waitDeletedResult(t, queued) // provider deliberately still holds the lane
	close(release)
	waitDeletedResult(t, first)
	if calls.Load() != 1 {
		t.Fatal("queued operation reached provider")
	}
}

type deletionEmbedding struct{ started, release chan struct{} }

func (*deletionEmbedding) Dimensions() int { return 1 }
func (e *deletionEmbedding) Embed(context.Context, []string) ([][]float32, error) {
	close(e.started)
	<-e.release
	return [][]float32{{1}}, nil
}
func TestDeletionAfterUncooperativeEmbeddingPreventsConversationModel(t *testing.T) {
	var calls atomic.Int64
	p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		calls.Add(1)
		return llm.Response{Content: "must not run"}, nil
	}}
	a, s := deletionAssistant(t, p)
	embedding := &deletionEmbedding{started: make(chan struct{}), release: make(chan struct{})}
	a.SetEmbedding(embedding, 0.5)
	done := make(chan error, 1)
	go func() {
		_, err := a.HandleMessage(context.Background(), topicMsg("tgconv:embedding-delete", "search meaningful history"))
		done <- err
	}()
	waitSignal(t, embedding.started)
	s.remove(t, "tgconv:embedding-delete")
	_ = a.CancelConversation(context.Background(), "tgconv:embedding-delete")
	close(embedding.release)
	waitDeletedResult(t, done)
	if calls.Load() != 0 {
		t.Fatal("late embedding admitted a model call")
	}
}
