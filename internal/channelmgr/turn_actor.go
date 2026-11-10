package channelmgr

import (
	"context"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// PrompterForActor returns an interaction bound to the immutable initiating actor.
func (m *Manager) PrompterForActor(ctx context.Context, conversation string, actor interact.Actor) interact.PromptAsker {
	if actor.Channel != "telegram" {
		return m.PrompterFor(conversation)
	}
	mc := m.findRunningForChat(ctx, conversation)
	if mc == nil || mc.tg == nil {
		return nil
	}
	return mc.tg.PrompterForActor(ctx, conversation, actor)
}

// ConfirmerForActor returns an approval interaction bound to the initiating actor.
func (m *Manager) ConfirmerForActor(ctx context.Context, conversation string, actor interact.Actor) interact.ConfirmationAsker {
	if actor.Channel != "telegram" {
		return m.ConfirmerFor(conversation)
	}
	mc := m.findRunningForChat(ctx, conversation)
	if mc == nil || mc.tg == nil {
		return nil
	}
	return mc.tg.ConfirmerForActor(ctx, conversation, actor)
}
