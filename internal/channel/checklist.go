package channel

import "context"

// ChecklistSender sends native Telegram checklists (Bot API 9.1). Implemented
// by channelmgr.Manager and backed by the Telegram channel; other channels
// degrade to plain-text lists. Task completion lives client-side in Telegram —
// this is a one-way mirror surface.
type ChecklistSender interface {
	// SendChecklistToChat sends a checklist with the given title and tasks.
	// Returns the sent message ID.
	SendChecklistToChat(ctx context.Context, chatID, title string, tasks []string) (int, error)
	// CanSendChecklists reports whether a native checklist can reach chatID.
	CanSendChecklists(chatID string) bool
}
