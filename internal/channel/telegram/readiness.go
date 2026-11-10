package telegram

import (
	"context"
	"errors"

	"github.com/go-telegram/bot"
)

// IdentityReadiness contains public identity/capability metadata only. It does
// not prove rendering, delivery, or permission for a particular mutation.
type IdentityReadiness struct {
	BotID                int64    `json:"bot_id"`
	CanonicalUsername    string   `json:"canonical_username"`
	ActiveUsernames      []string `json:"active_usernames,omitempty"`
	TopicsEnabled        bool     `json:"topics_enabled"`
	UsersCanCreateTopics bool     `json:"users_can_create_topics"`
	AliasLookupAvailable bool     `json:"alias_lookup_available"`
}

// InspectBotIdentity performs read-only identity and alias capability checks.
func InspectBotIdentity(ctx context.Context, b *bot.Bot) (IdentityReadiness, error) {
	me, e := b.GetMe(ctx)
	if e != nil {
		return IdentityReadiness{}, errors.New("telegram identity check failed")
	}
	result := IdentityReadiness{BotID: me.ID, CanonicalUsername: me.Username, TopicsEnabled: me.HasTopicsEnabled, UsersCanCreateTopics: me.AllowsUsersToCreateTopics}
	info, e := b.GetChat(ctx, &bot.GetChatParams{ChatID: me.ID})
	if e == nil && info.ID == me.ID {
		result.AliasLookupAvailable = true
		result.ActiveUsernames = append([]string{}, info.ActiveUsernames...)
	}
	return result, nil
}
