package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/securetoken"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
)

type providerFunc func(context.Context, llm.Request) (llm.Response, error)

func (f providerFunc) Complete(ctx context.Context, r llm.Request) (llm.Response, error) {
	return f(ctx, r)
}
func engineFixture(t *testing.T, p llm.Provider, tools []Tool) (*Engine, string, conversations.Conversation) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.sqlite")
	db, err := legacy.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err = db.CreateUser(context.Background(), &domain.User{ID: owner, Username: "owner", PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	signer, _ := securetoken.New(make([]byte, 32))
	repo, err := conversations.OpenExisting(context.Background(), path, signer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	e, err := New(repo, func(context.Context, string, string) (Profile, error) { return Profile{p, 1, "model-fixture"}, nil }, "ordinary mobile policy", tools)
	if err != nil {
		t.Fatal(err)
	}
	c, err := repo.Create(context.Background(), owner, "one", conversations.Settings{ProfileID: "default", MemoryMode: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	return e, owner, c
}
func accepted(t *testing.T, e *Engine, owner, conversation, text string) conversations.Turn {
	t.Helper()
	turn, _, err := e.Accept(context.Background(), owner, conversation, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}
func runOne(t *testing.T, e *Engine, owner string) {
	t.Helper()
	rt, err := e.Repo.AcquireRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Repo.ReleaseRuntime(context.Background(), rt)
	turn, f, err := e.Repo.Claim(context.Background(), rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	e.process(context.Background(), turn, f)
}
func TestIsolatedEngineCanonicalDedupAndNoCache(t *testing.T) {
	var calls atomic.Int32
	tool := Tool{Definition: llm.ToolDefinition{Name: "safe_mutation", InputSchema: json.RawMessage(`{"type":"object"}`)}, Revision: "1", Effect: "mutating", Isolated: true, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		calls.Add(1)
		return "committed", nil
	}}
	provider := providerFunc(func(ctx context.Context, r llm.Request) (llm.Response, error) {
		if r.CacheScope != "" || r.RoutingSnapshot != nil || len(r.History) != 0 || r.Message != "A" {
			t.Fatal("unsafe generic context")
		}
		if len(r.Tools) != 1 {
			t.Fatal("isolated policy exposed shared integration")
		}
		if len(r.ToolExchanges) == 0 {
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "one", Name: "safe_mutation", Input: json.RawMessage(`{"a":1,"b":2}`)}}}, nil
		}
		if len(r.ToolExchanges) == 1 {
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "new-model-id", Name: "safe_mutation", Input: json.RawMessage(`{"b":2,"a":1}`)}}}, nil
		}
		return llm.Response{Content: "done"}, nil
	})
	shared := Tool{Definition: llm.ToolDefinition{Name: "shared_memory"}, Revision: "1", Effect: "read", Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		t.Fatal("isolated shared memory leak")
		return "", nil
	}}
	e, owner, c := engineFixture(t, provider, []Tool{tool, shared})
	turn := accepted(t, e, owner, c.ID, "A")
	_ = accepted(t, e, owner, c.ID, "future B")
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil || got.State != "completed" || calls.Load() != 1 {
		t.Fatal("canonical mutation dedup", got, calls.Load(), err)
	}
}
func TestAmbiguousToolNeverRetriesAndProfileDriftStops(t *testing.T) {
	var calls atomic.Int32
	p := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "x", Name: "write", Input: json.RawMessage(`{}`)}}}, nil
	})
	tool := Tool{Definition: llm.ToolDefinition{Name: "write"}, Revision: "1", Effect: "unknown", Isolated: true, Execute: func(context.Context, conversations.Fence, json.RawMessage) (string, error) {
		calls.Add(1)
		return "", errors.New("response lost after mutation")
	}}
	e, owner, c := engineFixture(t, p, []Tool{tool})
	turn := accepted(t, e, owner, c.ID, "write")
	runOne(t, e, owner)
	got, _ := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if got.State != "unknown_outcome" || calls.Load() != 1 {
		t.Fatal("ambiguous effect retried", got)
	}
	turn = accepted(t, e, owner, c.ID, "policy changed")
	e.Resolve = func(context.Context, string, string) (Profile, error) { return Profile{p, 2, "new-destination"}, nil }
	runOne(t, e, owner)
	got, _ = e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if got.State != "failed" || calls.Load() != 1 {
		t.Fatal("policy silently replaced")
	}
}
func TestCancellationDoesNotCommitLateProviderResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		close(entered)
		<-release
		return llm.Response{Content: "late private text"}, nil
	})
	e, owner, c := engineFixture(t, p, nil)
	turn := accepted(t, e, owner, c.ID, "slow")
	rt, err := e.Repo.AcquireRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claimed, f, err := e.Repo.Claim(context.Background(), rt, owner)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { e.process(context.Background(), claimed, f); close(done) }()
	<-entered
	if _, err = e.Repo.Clear(context.Background(), owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("late provider stuck")
	}
	got, _ := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if got.State != "cancelled" || got.OutputMessageID != "" {
		t.Fatal("late callback committed", got)
	}
}

type savedMemoryFunc func(context.Context, string) (string, error)

func (f savedMemoryFunc) ReadSavedMemory(ctx context.Context, owner string) (string, error) {
	return f(ctx, owner)
}

func TestSavedMemoryFrozenOnlyForExplicitAccountMode(t *testing.T) {
	const canary = "ACCOUNT_KNOWLEDGE_CANARY"
	var reads atomic.Int32
	p := providerFunc(func(_ context.Context, r llm.Request) (llm.Response, error) {
		if r.Message == "isolated" && strings.Contains(r.SystemPrompt, canary) {
			t.Fatal("isolated memory leak")
		}
		if r.Message == "account" && !strings.Contains(r.SystemPrompt, canary) {
			t.Fatal("accepted knowledge snapshot changed")
		}
		return llm.Response{Content: "safe answer"}, nil
	})
	e, owner, c := engineFixture(t, p, nil)
	e.AccountMemory = savedMemoryFunc(func(context.Context, string) (string, error) { reads.Add(1); return canary, nil })
	accepted(t, e, owner, c.ID, "isolated")
	runOne(t, e, owner)
	if reads.Load() != 0 {
		t.Fatal("isolated mode read account knowledge")
	}
	account, err := e.Repo.Create(context.Background(), owner, "account", conversations.Settings{ProfileID: "default", MemoryMode: "account"})
	if err != nil {
		t.Fatal(err)
	}
	accepted(t, e, owner, account.ID, "account")
	e.AccountMemory = savedMemoryFunc(func(context.Context, string) (string, error) {
		t.Fatal("memory reloaded after acceptance")
		return "", nil
	})
	runOne(t, e, owner)
	if reads.Load() != 1 {
		t.Fatal("account knowledge not captured once")
	}
}

func TestHTTPMutationLostResponseRemainsUnknownAfterCoordinatorRestart(t *testing.T) {
	var mutations atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Error("unexpected method")
		}
		mutations.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer endpoint.Close()
	tool := Tool{Definition: llm.ToolDefinition{Name: "fixture_write"}, Revision: "1", Effect: "mutating", Isolated: true, Execute: func(ctx context.Context, _ conversations.Fence, _ json.RawMessage) (string, error) {
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint.URL, strings.NewReader("fixture"))
		response, err := endpoint.Client().Do(req)
		if response != nil {
			response.Body.Close()
		}
		return "", err
	}}
	p := providerFunc(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "write", Name: "fixture_write", Input: json.RawMessage(`{}`)}}}, nil
	})
	e, owner, c := engineFixture(t, p, []Tool{tool})
	turn := accepted(t, e, owner, c.ID, "mutation")
	runOne(t, e, owner)
	got, err := e.Repo.GetTurn(context.Background(), owner, c.ID, turn.ID)
	if err != nil || got.State != "unknown_outcome" || mutations.Load() != 1 {
		t.Fatal("ambiguous mutation outcome", got, err)
	}
	runtime, err := e.Repo.AcquireRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Repo.ReleaseRuntime(context.Background(), runtime)
	if _, _, err = e.Repo.Claim(context.Background(), runtime, owner); err == nil {
		t.Fatal("ambiguous write queued after restart")
	}
	if mutations.Load() != 1 {
		t.Fatal("unsafe mutation replay")
	}
}
