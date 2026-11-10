package telegram

import (
	"context"
	"time"
)

func (c *Channel) handleCompact(root context.Context, tgChatID int64, chatID string) {
	target, err := c.resolveTarget(root, chatID)
	if err != nil || !target.Private || target.ThreadID == 0 {
		c.sendSingleMessage(tgChatID, "Откройте тему и отправьте /compact / Open a topic and send /compact", 0)
		return
	}
	if c.compactFn == nil {
		c.sendSingleMessage(tgChatID, "Сжатие контекста недоступно / Context compression unavailable", 0, target.ThreadID)
		return
	}
	ctx, cancel := context.WithTimeout(root, 2*time.Minute)
	defer cancel()
	text, err := c.compactFn(ctx, chatID)
	if err != nil {
		text = "Не удалось сжать контекст. История сохранена / Could not compact context; history preserved"
	}
	if _, err = c.resolveTarget(root, chatID); err == nil {
		c.sendSingleMessage(tgChatID, text, 0, target.ThreadID)
	}
}
