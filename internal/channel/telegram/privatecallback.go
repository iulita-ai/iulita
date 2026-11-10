package telegram

import (
	"context"
	"errors"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// SendPrivateCallbackReply delivers only to the verified caller of a callback.
// Text must already pass the application's owner and privacy policy. There is
// deliberately no ordinary-message retry on delivery failure.
func (c *Channel) SendPrivateCallbackReply(ctx context.Context, cq *models.CallbackQuery, text string) error {
	if cq == nil || cq.ID == "" || cq.From.ID == 0 || !c.isAllowed(cq.From.ID) {
		return errors.New("callback actor unavailable")
	}
	if cq.Message.Message != nil && cq.Message.Message.DirectMessagesTopic != nil {
		return errors.New("private callback replies unsupported in this chat")
	}
	_, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{Text: text, EphemeralMessageParameters: &models.EphemeralMessageParameters{CallbackQueryID: cq.ID}})
	if err != nil {
		return errors.New("private callback delivery failed")
	}
	return nil
}
