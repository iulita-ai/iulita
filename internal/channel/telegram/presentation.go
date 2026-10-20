package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"go.uber.org/zap"
)

// verbatimParams constructs literal sends, including the secret-response path.
// No Markdown/HTML parser, entity annotation or keyboard can alter the text.
func verbatimParams(chatID int64, text string, replyTo int, receiverUserID int64) []*bot.SendMessageParams {
	chunks := channel.VerbatimChunks(text, 4096)
	params := make([]*bot.SendMessageParams, 0, len(chunks))
	for index, chunk := range chunks {
		p := &bot.SendMessageParams{
			ChatID:             chatID,
			Text:               chunk,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
		}
		if index == 0 && replyTo > 0 {
			p.ReplyParameters = &models.ReplyParameters{MessageID: replyTo}
		}
		if receiverUserID != 0 {
			p.EphemeralMessageParameters = &models.EphemeralMessageParameters{ReceiverUserID: receiverUserID}
		}
		params = append(params, p)
	}
	return params
}

func (c *Channel) sendVerbatimResponse(ctx context.Context, chatID int64, text string, replyTo int, receiverUserID int64) {
	for _, params := range verbatimParams(chatID, text, replyTo, receiverUserID) {
		message, err := c.bot.SendMessage(ctx, params)
		if err != nil {
			// Do not expose provider errors or retry a sensitive response as a
			// public message when ephemeral delivery fails.
			c.logger.Error("failed to deliver literal response", zap.Int64("chat_id", chatID))
			return
		}
		if c.sentMsgs != nil {
			c.sentMsgs.record(chatID, message.ID, params.Text)
		}
	}
}
