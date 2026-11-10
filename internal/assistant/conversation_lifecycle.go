package assistant

import (
	"context"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
)

type conversationAvailability interface {
	ConversationUnavailable(context.Context, string) (bool, error)
}
type conversationLifetimeKey struct{}
type conversationRun struct{ cancel context.CancelCauseFunc }

// guardConversation fails closed on both lifecycle read failures and deletion.
// The separate lifetime survives deadline extension and detached finalization.
func (a *Assistant) guardConversation(ctx context.Context, key string) error {
	if err := conversationContextError(ctx); err != nil {
		return err
	}
	if store, ok := a.store.(conversationAvailability); ok {
		check, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		unavailable, err := store.ConversationUnavailable(check, key)
		if err != nil {
			return fmt.Errorf("check conversation lifecycle: %w", err)
		}
		if unavailable {
			return domain.ErrConversationDeleted
		}
	}
	return nil
}

func conversationContextError(ctx context.Context) error {
	if life, ok := ctx.Value(conversationLifetimeKey{}).(context.Context); ok && life.Err() != nil {
		return context.Cause(life)
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return nil
}

// beginConversation registers before taking the conversation lane so deletion
// also cancels queued operations. Registry identity prevents stale unregister.
func (a *Assistant) beginConversation(ctx context.Context, key string) (context.Context, func(), error) {
	if err := a.guardConversation(ctx, key); err != nil {
		return nil, nil, err
	}
	life, lifeCancel := context.WithCancelCause(context.WithoutCancel(ctx))
	request, requestCancel := context.WithCancelCause(ctx)
	run := &conversationRun{cancel: func(err error) { lifeCancel(err); requestCancel(err) }}
	request = context.WithValue(request, conversationLifetimeKey{}, life)
	request = skill.WithDeadlineExtender(request, skill.LifecycleExtender(life))
	stopRoot := func() bool { return false }
	if a.lifecycleRoot != nil {
		stopRoot = context.AfterFunc(a.lifecycleRoot, func() { run.cancel(context.Cause(a.lifecycleRoot)) })
	}
	a.activeConversationMu.Lock()
	if a.activeConversations == nil {
		a.activeConversations = map[string]map[*conversationRun]struct{}{}
	}
	if a.activeConversations[key] == nil {
		a.activeConversations[key] = map[*conversationRun]struct{}{}
	}
	a.activeConversations[key][run] = struct{}{}
	a.activeConversationMu.Unlock()
	stop := func() {
		stopRoot()
		run.cancel(context.Canceled)
		a.activeConversationMu.Lock()
		delete(a.activeConversations[key], run)
		if len(a.activeConversations[key]) == 0 {
			delete(a.activeConversations, key)
		}
		a.activeConversationMu.Unlock()
	}
	// Close the registration race: a deletion committed before the registry entry
	// must still prevent the first read/provider/tool invocation.
	if err := a.guardConversation(request, key); err != nil {
		stop()
		return nil, nil, err
	}
	if _, ok := a.store.(conversationAvailability); ok {
		go func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-life.Done():
					return
				case <-ticker.C:
					if err := a.guardConversation(life, key); err != nil {
						run.cancel(err)
						return
					}
				}
			}
		}()
	}
	return request, stop, nil
}

// CancelConversation is called only after the owner's durable tombstone and
// cleanup commit. It never waits for the lane held by a model or prompt.
func (a *Assistant) CancelConversation(_ context.Context, key string) error {
	if err := domain.CheckLegacyChatID(key); err != nil {
		return err
	}
	a.activeConversationMu.Lock()
	for run := range a.activeConversations[key] {
		run.cancel(domain.ErrConversationDeleted)
	}
	a.activeConversationMu.Unlock()
	if a.approvals != nil {
		a.approvals.take(key)
	}
	return nil
}
