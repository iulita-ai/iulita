package telegram

import (
	"context"
	"fmt"
	"sync"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/i18n"
	"github.com/iulita-ai/iulita/internal/security"
)

const maxBufferedTelegramResponse = 256 * 1024

// bufferedStream deliberately never transmits unclassified provider partials.
// The interface remains compatible with StreamingSender; final publication is
// exactly once, after whole-response classification. Cancellation drops output.
func (c *Channel) bufferedStream(ctx context.Context, key string, replyTo int, owner string) (editFn, doneFn func(string), err error) {
	target, err := c.resolveTarget(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	done, tooLarge := false, false
	edit := func(snapshot string) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		if len(snapshot) > maxBufferedTelegramResponse {
			tooLarge = true
		}
	}
	finish := func(text string) {
		mu.Lock()
		if done {
			mu.Unlock()
			return
		}
		done = true
		reject := tooLarge || len(text) > maxBufferedTelegramResponse
		mu.Unlock()
		if channel.HasResponsePresentation(ctx) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		var recheckErr error
		target, recheckErr = c.resolveTarget(ctx, key)
		if recheckErr != nil {
			return
		}
		locale := i18n.LocaleFrom(ctx).String()
		neutral := func() {
			c.sendSingleMessage(target.ChatID, i18n.T(ctx, "TelegramErrorResponse"), replyTo, target.ThreadID)
		}
		if reject {
			neutral()
			return
		}
		if matched, _ := security.Contains(text); matched {
			actor := int64(0)
			if target.Private {
				actor = target.ChatID
			}
			if actor <= 0 {
				neutral()
				return
			}
			if err := c.sendVerbatimResponse(ctx, target.ChatID, text, replyTo, actor, target.ThreadID); err != nil {
				neutral()
			}
			return
		}
		safe := security.RedactPaymentData(text)
		if owner != "" && c.rememberSvc != nil {
			c.sendResponseWithBookmark(target.ChatID, safe, replyTo, key, owner, locale)
		} else {
			c.sendResponse(target.ChatID, safe, replyTo, target.ThreadID)
		}
	}
	if target.ChatID == 0 {
		return nil, nil, fmt.Errorf("invalid stream target")
	}
	return edit, finish, nil
}
