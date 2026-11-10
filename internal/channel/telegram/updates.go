package telegram

import (
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Explicitly replace Telegram's remembered subscription with every known update
// surface. Nil silently reuses a server-side filter from an older deployment.
// Keeping all SDK update kinds avoids narrowing earlier subscriptions; dispatch
// still independently authenticates and selects supported content.
func telegramAllowedUpdates() bot.AllowedUpdates {
	return bot.AllowedUpdates{
		models.AllowedUpdateMessage,
		models.AllowedUpdateEditedMessage,
		models.AllowedUpdateChannelPost,
		models.AllowedUpdateEditedChannelPost,
		models.AllowedUpdateBusinessConnection,
		models.AllowedUpdateBusinessMessage,
		models.AllowedUpdateEditedBusinessMessage,
		models.AllowedUpdateDeletedBusinessMessages,
		models.AllowedUpdateMessageReaction,
		models.AllowedUpdateMessageReactionCount,
		models.AllowedUpdateInlineQuery,
		models.AllowedUpdateChosenInlineResult,
		models.AllowedUpdateCallbackQuery,
		models.AllowedUpdateWidgetQuery,
		models.AllowedUpdateSharedWidgetQuery,
		models.AllowedUpdateShippingQuery,
		models.AllowedUpdatePreCheckoutQuery,
		models.AllowedUpdatePurchasedPaidMedia,
		models.AllowedUpdatePoll,
		models.AllowedUpdatePollAnswer,
		models.AllowedUpdateMyChatMember,
		models.AllowedUpdateChatMember,
		models.AllowedUpdateChatJoinRequest,
		models.AllowedUpdateChatBoost,
		models.AllowedUpdateRemovedChatBoost,
		models.AllowedUpdateManagedBot,
		models.AllowedUpdateGuestMessage,
		models.AllowedUpdateSubscription,
		models.AllowedUpdateStoppedMessageGeneration,
	}
}
