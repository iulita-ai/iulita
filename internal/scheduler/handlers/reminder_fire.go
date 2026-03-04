package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/storage"
)

const TaskTypeReminderFire = "reminder.fire"

type reminderPayload struct {
	ReminderID int64  `json:"reminder_id"`
	ChatID     string `json:"chat_id"`
	Title      string `json:"title"`
	DueAt      string `json:"due_at"`
	Timezone   string `json:"timezone"`
}

// ReminderFireHandler sends a reminder notification and marks it as fired.
type ReminderFireHandler struct {
	store  storage.Repository
	sender channel.MessageSender
}

func NewReminderFireHandler(store storage.Repository, sender channel.MessageSender) *ReminderFireHandler {
	return &ReminderFireHandler{store: store, sender: sender}
}

func (h *ReminderFireHandler) Type() string { return TaskTypeReminderFire }

func (h *ReminderFireHandler) Handle(ctx context.Context, payload string) (string, error) {
	var p reminderPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", fmt.Errorf("invalid payload: %w", err)
	}

	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}

	dueAt, _ := time.Parse(time.RFC3339, p.DueAt)
	dueLocal := dueAt.In(loc)

	text := fmt.Sprintf("⏰ Reminder: %s\n(was set for %s)", p.Title, dueLocal.Format("15:04 02.01.2006"))
	if err := h.sender.SendMessage(ctx, p.ChatID, text); err != nil {
		return "", fmt.Errorf("sending reminder: %w", err)
	}

	if err := h.store.MarkReminderFired(ctx, p.ReminderID); err != nil {
		return "", fmt.Errorf("marking reminder fired: %w", err)
	}

	return `{"status":"fired"}`, nil
}
