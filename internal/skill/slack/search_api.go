package slack

import (
	"context"

	slackapi "github.com/slack-go/slack"
)

// searchAPI is the narrow set of READ-ONLY slack-go calls the search skill uses.
// Isolating them behind an interface lets tests inject canned responses without
// a live Slack client. There is deliberately no write method here — the skill
// must never post, edit, or react (read-only invariant).
type searchAPI interface {
	SearchMessagesContext(ctx context.Context, query string, params slackapi.SearchParameters) (*slackapi.SearchMessages, error)
	GetConversationHistoryContext(ctx context.Context, params *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(ctx context.Context, params *slackapi.GetConversationRepliesParameters) (msgs []slackapi.Message, hasMore bool, nextCursor string, err error)
}

// slackClientAdapter adapts a *slackapi.Client to searchAPI. The methods already
// match; the wrapper exists only to satisfy the interface as a named type.
type slackClientAdapter struct{ *slackapi.Client }
