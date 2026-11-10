package telegram

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ProductionInlineWidgetsAvailable is intentionally not configurable. Telegram
// currently limits these surfaces to its test server. Opening the production
// gate requires a separately reviewed implementation and official availability.
func ProductionInlineWidgetsAvailable() bool { return false }

// Shared queries omit chat/message coordinates. Never infer an owner route from
// From, widget ID, or a previous private card; reply without loading user data.
func (c *Channel) handleUnavailableWidgetSurface(ctx context.Context, u *models.Update) bool {
	if u.SharedWidgetQuery != nil {
		q := u.SharedWidgetQuery
		if c.inlineEnabled() {
			c.answerPublicShared(ctx, q)
			return true
		}
		if _, deliveryErr := c.bot.AnswerWidgetQuery(ctx, &bot.AnswerWidgetQueryParams{WidgetQueryID: q.ID, Data: dayText(q.From.LanguageCode, "Shared widgets are unavailable. Open the private bot chat.", "Общие виджеты пока недоступны. Откройте личный чат с ботом.")}); deliveryErr != nil {
			c.noteFailure("Telegram AnswerWidgetQuery failed", deliveryErr)
		}
		return true
	}
	if u.InlineQuery != nil && c.inlineEnabled() {
		c.answerPublicInline(ctx, u.InlineQuery)
		return true
	}
	if u.InlineQuery != nil && !ProductionInlineWidgetsAvailable() {
		if _, deliveryErr := c.bot.AnswerInlineQuery(ctx, &bot.AnswerInlineQueryParams{InlineQueryID: u.InlineQuery.ID, Results: []models.InlineQueryResult{}, IsPersonal: true}); deliveryErr != nil {
			c.noteFailure("Telegram AnswerInlineQuery failed", deliveryErr)
		}
		return true
	}
	return false
}
