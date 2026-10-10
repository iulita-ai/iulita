package channelmgr

import (
	"context"
)

// SendChecklistToChat sends a native Telegram checklist to a chat. Implements
// channel.ChecklistSender. Routing reuses the fail-closed location resolver:
// DB-bound chats resolve to their own bot, ambiguous multi-bot setups refuse.
func (m *Manager) SendChecklistToChat(ctx context.Context, chatID, title string, tasks []string) (int, error) {
	tg, chatID64, err := m.locationTargetFor(ctx, chatID)
	if err != nil {
		return 0, err
	}
	return tg.SendChecklist(ctx, chatID64, title, tasks)
}

// CanSendChecklists reports whether a native checklist can reach chatID.
// Implements channel.ChecklistSender.
func (m *Manager) CanSendChecklists(chatID string) bool {
	_, _, err := m.locationTargetFor(context.Background(), chatID)
	return err == nil
}
