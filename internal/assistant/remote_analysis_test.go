package assistant

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage"
	"go.uber.org/zap"
)

func TestSSHAnalysisTaintBlocksMemoryExfiltrationAndDelegationBeforeDispatch(t *testing.T) {
	ctx := skill.WithTurnTaint(context.Background())
	skill.MarkRemoteOutputUsed(ctx)
	// An empty assistant would panic on registry access. Tainted forbidden calls
	// must return before dispatch, approval or logging, including within a batch.
	a := &Assistant{}
	for _, name := range []string{"remember", "forget", "http_request", "webfetch", "delegate", "orchestrate", "schedule", "remote_runbook", "remote_files", "remote_background", "share_location", "translate", "directives"} {
		result := a.executeSkill(ctx, "chat", llm.ToolCall{ID: "injected", Name: name}, 0)
		if !result.IsError {
			t.Fatalf("%s executed after SSH output", name)
		}
	}
	for _, name := range []string{"remote_exec", "remote_chain", "remote_status", "remote_cancel", "datetime", "ask_user"} {
		if !remoteAnalysisToolAllowed(name) {
			t.Fatalf("diagnostic tool %s blocked", name)
		}
	}
	if skill.RemoteOutputUsedInTurn(skill.WithTurnTaint(ctx)) {
		t.Fatal("SSH taint escaped its turn")
	}
}

func TestSSHAnalysisDoesNotRunMutatingHooksOrLearnFromRemoteText(t *testing.T) {
	ctx := skill.WithTurnTaint(context.Background())
	skill.MarkRemoteOutputUsed(ctx)
	store := newSynthTestStore(t)
	a := New(nil, store, skill.NewRegistry(), "test", "", 200000, zap.NewNop())
	a.SetSelfImprove(true, 1)
	hook := &modifyingHook{}
	a.AddPostHook(hook)
	out, err := a.finishResponse(ctx, "chat", "test-user", "Cluster degraded: a node is NotReady", false, 3, nil)
	if err != nil || hook.calls != 0 || out != "Cluster degraded: a node is NotReady" {
		t.Fatalf("mutated analysis: %q %v hooks%d", out, err, hook.calls)
	}
	// Also covers the max-iteration path, which directly queues a review.
	a.maybeEnqueueSkillReview(ctx, "chat", "test-user", 9, 100, "remote_exec -> ok")
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{Type: "skill.review", Limit: 10})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("analysis learned automatically: %v %v", tasks, err)
	}
	history, err := store.GetHistory(ctx, "chat", 10)
	if err != nil || len(history) != 1 || history[0].Content != out {
		t.Fatal("normal user-visible analysis history lost")
	}
	defs := remoteAnalysisTools([]llm.ToolDefinition{{Name: "remember"}, {Name: "remote_exec"}, {Name: "http_request"}, {Name: "remote_status"}})
	if len(defs) != 2 || defs[0].Name != "remote_exec" || defs[1].Name != "remote_status" {
		t.Fatalf("unsafe definitions %+v", defs)
	}
}
