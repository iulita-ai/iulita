package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
)

type conversationLane struct {
	token chan struct{}
	users int
}

func (a *Assistant) lockConversation(ctx context.Context, key string) (func(), error) {
	a.conversationMu.Lock()
	if a.conversations == nil {
		a.conversations = map[string]*conversationLane{}
	}
	lane := a.conversations[key]
	if lane == nil {
		lane = &conversationLane{token: make(chan struct{}, 1)}
		lane.token <- struct{}{}
		a.conversations[key] = lane
	}
	lane.users++
	a.conversationMu.Unlock()
	drop := func() {
		a.conversationMu.Lock()
		lane.users--
		if lane.users == 0 {
			delete(a.conversations, key)
		}
		a.conversationMu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, context.Cause(ctx)
	case <-lane.token:
	}
	release := func() { lane.token <- struct{}{}; drop() }
	if e := ctx.Err(); e != nil {
		release()
		return nil, context.Cause(ctx)
	}
	return release, nil
}
func topicContext(msg channel.IncomingMessage) bool {
	return msg.ChannelType == "telegram" && msg.ProvenPrivateRoute && strings.HasPrefix(msg.ChatID, "tgconv:")
}

// ClearConversation linearizes against turns and compaction in exactly this
// conversation. Call asynchronously from a polling dispatcher awaiting callbacks.
func (a *Assistant) ClearConversation(ctx context.Context, key string) error {
	if e := domain.CheckLegacyChatID(key); e != nil {
		return e
	}
	ctx, stop, lifecycleErr := a.beginConversation(ctx, key)
	if lifecycleErr != nil {
		return lifecycleErr
	}
	defer stop()
	release, e := a.lockConversation(ctx, key)
	if e != nil {
		return e
	}
	defer release()
	if e := a.guardConversation(ctx, key); e != nil {
		return e
	}
	if e := a.store.ClearHistory(ctx, key); e != nil {
		return e
	}
	// Pending legacy tool approvals must not execute against cleared context.
	if a.approvals != nil {
		a.approvals.take(key)
	}
	return nil
}

// CompactContext manually summarizes the conversation and reports its committed estimated size.
func (a *Assistant) CompactContext(ctx context.Context, key string) (string, error) {
	if e := domain.CheckLegacyChatID(key); e != nil {
		return "", e
	}
	ctx, stop, lifecycleErr := a.beginConversation(ctx, key)
	if lifecycleErr != nil {
		return "", lifecycleErr
	}
	defer stop()
	release, e := a.lockConversation(ctx, key)
	if e != nil {
		return "", e
	}
	defer release()
	if err := a.guardConversation(ctx, key); err != nil {
		return "", err
	}
	ctx = context.WithValue(ctx, manualCompressionKey{}, true)
	before, e := a.store.GetHistory(ctx, key, 0)
	if e != nil {
		return "", fmt.Errorf("load conversation: %w", e)
	}
	minimum := 4
	if strings.HasPrefix(key, "tgconv:") {
		minimum = 2
	}
	req := conversationContextRequest(key, before)
	if p, ok := a.provider.(llm.ProfileInvoker); ok {
		req.RoutingSnapshot = p.AcquireProfileSnapshot()
	}
	window := a.compressionContextWindow(req)
	if len(before) < minimum {
		return compactNoop(ctx, before, window), nil
	}
	after, e := a.compressIfNeeded(ctx, key, before, forcedCompressionTokens(window), req)
	if e != nil {
		return "", e
	}
	if estimatedHistoryTokens(after) >= estimatedHistoryTokens(before) {
		return compactNoop(ctx, before, window), nil
	}
	return compressionReport(ctx, before, after, a.compressionContextWindow(req)), nil
}
