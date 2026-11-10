package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/iulita-ai/iulita/internal/dayview"
)

// dayOutbox stores desired display separately from operation outcome. Restarting
// delivery never replays a provider mutation.
type dayOutbox interface {
	PendingDayCards(context.Context, string) ([]dayview.Card, error)
	MarkDayDelivered(context.Context, string, int64) error
}

func (c *Channel) runDayDelivery(ctx context.Context) {
	if c.day == nil {
		return
	}
	store, ok := c.day.Store.(dayOutbox)
	if !ok {
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cards, e := store.PendingDayCards(ctx, c.instanceID)
			if e != nil {
				continue
			}
			for cardIndex := range cards {
				card := &cards[cardIndex]
				c.day.mu.Lock()
				fresh, err := c.day.Store.GetDayCard(ctx, card.ID)
				c.day.mu.Unlock()
				if err != nil || fresh == nil {
					continue
				}
				card = fresh

				if ctx.Err() != nil {
					return
				}
				if card.Message == 0 || card.DesiredState == "" {
					continue
				}
				var delivery cardData
				if json.Unmarshal([]byte(card.ItemsJSON), &delivery) != nil {
					continue
				}
				if _, routeErr := c.resolveTarget(ctx, delivery.Conversation); routeErr != nil {
					if deliveryErr := store.MarkDayDelivered(ctx, card.ID, card.Revision); deliveryErr != nil {
						c.noteFailure("Telegram MarkDayDelivered failed", deliveryErr)
					}
					continue
				}
				call, cancel := context.WithTimeout(ctx, 10*time.Second)
				e = c.deliverDayCard(call, card)
				cancel()
				if e == nil || containsNotModified(e) {
					if deliveryErr := store.MarkDayDelivered(ctx, card.ID, card.Revision); deliveryErr != nil {
						c.noteFailure("Telegram MarkDayDelivered failed", deliveryErr)
					}
					continue
				}
				if strings.Contains(e.Error(), "message to edit not found") || strings.Contains(e.Error(), "message can't be edited") {
					if deliveryErr := store.MarkDayDelivered(ctx, card.ID, card.Revision); deliveryErr != nil {
						c.noteFailure("Telegram MarkDayDelivered failed", deliveryErr)
					}
					continue
				}
				var flood *bot.TooManyRequestsError
				if errors.As(e, &flood) {
					wait := time.Duration(flood.RetryAfter) * time.Second
					timer := time.NewTimer(wait)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					break
				}
			}
		}
	}
}

func containsNotModified(e error) bool { return strings.Contains(e.Error(), "message is not modified") }

func (c *Channel) deliverDayCard(ctx context.Context, card *dayview.Card) error {
	var data cardData
	if e := json.Unmarshal([]byte(card.ItemsJSON), &data); e != nil {
		return e
	}
	if _, e := c.resolveTarget(ctx, data.Conversation); e != nil {
		return e
	}
	if data.TextMode {
		text, kb, e := dayTextView(card, data)
		if e != nil {
			return e
		}
		_, e = c.bot.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: card.Chat, MessageID: card.Message, Text: text, ReplyMarkup: kb})
		return e
	}
	_, e := c.bot.EditMessageWidgetData(ctx, &bot.EditMessageWidgetDataParams{ChatID: card.Chat, MessageID: card.Message, ChatData: &card.DesiredState})
	return e
}
