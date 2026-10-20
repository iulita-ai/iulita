package slack

import (
	"context"

	"github.com/iulita-ai/iulita/internal/channel"
	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"
)

func (c *Channel) sendVerbatimResponse(ctx context.Context, chatID, text string) {
	meta := c.resolveMeta(ctx, chatID)
	if meta == nil {
		c.logger.Warn("slack: unroutable chat id for literal response", zap.String("chat_id", chatID))
		return
	}
	for _, chunk := range channel.VerbatimChunks(text, maxMessageLen) {
		block := slackapi.NewSectionBlock(slackapi.NewTextBlockObject("plain_text", chunk, false, false), nil, nil)
		opts := []slackapi.MsgOption{
			slackapi.MsgOptionText(chunk, false), // Literal accessibility fallback.
			slackapi.MsgOptionBlocks(block),
			slackapi.MsgOptionDisableMarkdown(),
			slackapi.MsgOptionParse(false),
			slackapi.MsgOptionDisableLinkUnfurl(),
			slackapi.MsgOptionDisableMediaUnfurl(),
		}
		if meta.threadTS != "" {
			opts = append(opts, slackapi.MsgOptionTS(meta.threadTS))
		}
		if _, _, err := c.client.PostMessageContext(ctx, meta.channelID, opts...); err != nil {
			c.logger.Error("slack: failed to deliver literal response", zap.String("chat_id", chatID))
			return
		}
	}
}
