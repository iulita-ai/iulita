package telegram

import (
	"context"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/iulita-ai/iulita/internal/i18n"

	"github.com/go-telegram/bot/models"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/security"
)

func normalizedTelegramContent(m *models.Message) (string, *channel.QuoteContext) {
	if m == nil {
		return "", nil
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if m.RichMessage != nil {
		text = richPlain(m.RichMessage)
	}
	if m.Quote == nil {
		return text, nil
	}
	quoted := m.Quote.Text
	var entities []security.TextEntity
	for _, e := range m.Quote.Entities {
		entities = append(entities, security.TextEntity{Type: string(e.Type), Offset: e.Offset, Length: e.Length})
	}
	quoted = security.RedactPaymentEntities(quoted, entities)
	if len(m.Quote.RichTexts) > 0 {
		quoted = richPlain(m.Quote.RichTexts)
	}
	if len(quoted) > 4096 {
		quoted = quoted[:4096]
		for !utf8.ValidString(quoted) {
			quoted = quoted[:len(quoted)-1]
		}
	}
	q := &channel.QuoteContext{Text: quoted, Position: m.Quote.Position}
	if source := m.ReplyToMessage; source != nil && source.Chat.ID == m.Chat.ID && source.MessageThreadID == m.MessageThreadID {
		q.SourceMessageID = source.ID
		q.SourceRich = source.RichMessage != nil
		q.SourceChatID = source.Chat.ID
		q.SourceThreadID = source.MessageThreadID
		sourceText := source.Text
		if sourceText == "" {
			sourceText = source.Caption
		}
		if source.RichMessage != nil {
			sourceText = richPlain(source.RichMessage)
		}
		q.Verified = quoteAtUTF16(sourceText, quoted, m.Quote.Position)
	}
	return text, q
}
func quoteAtUTF16(source, quote string, position int) bool {
	if quote == "" || position < 0 {
		return false
	}
	s := utf16.Encode([]rune(source))
	q := utf16.Encode([]rune(quote))
	if position > len(s) || len(q) > len(s)-position {
		return false
	}
	for i, v := range q {
		if s[position+i] != v {
			return false
		}
	}
	return true
}

// sendRichQuoteReply preserves a verified selection and the existing bookmark
// affordance. Unsupported rich delivery falls back through the caller's ordinary
// response path. Sensitive/verbatim responses must bypass this helper.
func (c *Channel) sendRichQuoteReply(ctx context.Context, target channel.TelegramTarget, response string, q *channel.QuoteContext, conversation, owner, locale string, bookmark bool, replyTo ...int) bool {
	if q != nil && (!q.Verified || q.SourceChatID != target.ChatID || q.SourceThreadID != target.ThreadID) {
		return false
	}
	if len(response) > 12000 {
		return false
	}
	var reply *models.ReplyParameters
	if len(replyTo) > 0 && replyTo[0] > 0 {
		reply = &models.ReplyParameters{MessageID: replyTo[0]}
	}
	if q != nil && q.Verified && q.SourceChatID == target.ChatID && q.SourceThreadID == target.ThreadID {
		reply = &models.ReplyParameters{MessageID: q.SourceMessageID, QuotePosition: q.Position}
		if q.SourceRich {
			reply.QuoteRichTexts = []models.RichText{{PlainText: q.Text}}
		} else {
			reply.Quote = q.Text
		}
	}

	var markup models.ReplyMarkup
	cb := ""
	if bookmark && c.rememberSvc != nil {
		cb = "remember:" + generateNonce()
		tag := i18n.ResolveLocale(locale, "en")
		markup = bookmarkKeyboard(i18n.Tl(tag, "BookmarkButton"), i18n.Tl(tag, "CopyButton"), cb, response)
	}
	rendered, renderErr := renderRichMessage(response)
	if renderErr != nil {
		return false
	}
	sent, err := c.bot.SendRichMessage(ctx, &bot.SendRichMessageParams{ChatID: target.ChatID, MessageThreadID: target.ThreadID, RichMessage: rendered, ReplyParameters: reply, ReplyMarkup: markup})
	if err != nil {
		return false
	}
	if c.sentMsgs != nil {
		c.sentMsgs.record(target.ChatID, sent.ID, response, target.ThreadID)
	}
	if cb != "" {
		c.remembers.store(cb, &rememberEntry{tgChatID: target.ChatID, tgUserID: c.bookmarkActor(conversation, target.ChatID), msgID: sent.ID, content: response, chatID: conversation, userID: owner, locale: locale, createdAt: time.Now()})
	}
	return true
}
