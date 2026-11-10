package telegram

import (
	"context"
	"strconv"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

func (c *Channel) actorTarget(ctx context.Context, conversation string, actor interact.Actor) (channel.TelegramTarget, int64, bool) {
	target, e := c.resolveTarget(ctx, conversation)
	uid, parseErr := strconv.ParseInt(actor.ExternalID, 10, 64)
	if e != nil || parseErr != nil || uid <= 0 || actor.UserID == "" || actor.Channel != "telegram" || actor.ChannelInstance != c.instanceID || !c.isAllowed(uid) {
		return target, 0, false
	}
	if !target.Private || target.OwnerID != actor.UserID || target.ChatID != uid {
		return target, 0, false
	}
	return target, uid, true
}

// PrompterForActor captures the immutable initiating actor and topic.
func (c *Channel) PrompterForActor(ctx context.Context, conversation string, actor interact.Actor) interact.PromptAsker {
	target, uid, ok := c.actorTarget(ctx, conversation, actor)
	if !ok {
		return nil
	}
	return &telegramPrompter{channel: c, chatID: target.ChatID, threadID: target.ThreadID, actor: uid, prompts: c.prompts}
}

// ConfirmerForActor captures the immutable initiating actor and topic.
func (c *Channel) ConfirmerForActor(ctx context.Context, conversation string, actor interact.Actor) interact.ConfirmationAsker {
	target, uid, ok := c.actorTarget(ctx, conversation, actor)
	if !ok {
		return nil
	}
	return &telegramConfirmer{channel: c, chatID: target.ChatID, threadID: target.ThreadID, fromTg: uid}
}
