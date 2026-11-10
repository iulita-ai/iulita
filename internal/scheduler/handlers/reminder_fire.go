package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
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

	// Re-read current state: queued payloads are snapshots, not authority.
	if current, ok := h.store.(interface {
		ClaimReminderDelivery(context.Context, int64, string, time.Time) (*domain.Reminder, error)
	}); ok {
		due, parseErr := time.Parse(time.RFC3339, p.DueAt)
		if parseErr != nil {
			return "", parseErr
		}
		r, err := current.ClaimReminderDelivery(ctx, p.ReminderID, p.ChatID, due)
		if err != nil {
			return "", err
		}
		if r == nil {
			return `{"status":"superseded"}`, nil
		}
		p.Title = r.Title
		p.Timezone = r.Timezone
	}

	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}

	dueAt, _ := time.Parse(time.RFC3339, p.DueAt)
	dueLocal := dueAt.In(loc)

	text := fmt.Sprintf("⏰ Reminder: %s\n(was set for %s)", p.Title, dueLocal.Format("15:04 02.01.2006"))
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if err := h.sender.SendMessage(ctx, p.ChatID, text); err != nil {
		if errors.Is(err, domain.ErrConversationDeleted) {
			// Definitively no delivery; deletion already cancelled the bound row.
			return `{"status":"cancelled","reason":"topic_deleted"}`, nil
		}
		if errors.Is(err, channel.ErrDeliveryPaused) {
			if paused, ok := h.store.(interface {
				MarkReminderDeliverySuppressed(context.Context, int64) error
			}); ok {
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancel()
				if e := paused.MarkReminderDeliverySuppressed(saveCtx, p.ReminderID); e != nil {
					return "", e
				}
				return `{"status":"suppressed","reason":"topic_paused"}`, nil
			}
			return "", fmt.Errorf("reminder delivery paused: %w", err)
		}

		if uncertain, ok := h.store.(interface {
			MarkReminderDeliveryUnknown(context.Context, int64) error
		}); ok {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if saveErr := uncertain.MarkReminderDeliveryUnknown(saveCtx, p.ReminderID); saveErr != nil {
				return "", fmt.Errorf("sending reminder: %w; recording uncertain delivery: %w", err, saveErr)
			}
		}
		return "", fmt.Errorf("sending reminder: %w", err)
	}

	if err := h.store.MarkReminderFired(ctx, p.ReminderID); err != nil {
		return "", fmt.Errorf("marking reminder fired: %w", err)
	}

	return `{"status":"fired"}`, nil
}
