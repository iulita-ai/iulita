package engine

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

// RunReminders delivers into the durable native transcript, so an offline phone
// catches up through the existing snapshot/event protocol after reconnecting.
func (e *Engine) RunReminders(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.VerifyAuthority == nil {
				continue
			}
			ids, err := e.Repo.DueReminders(ctx)
			if err != nil {
				continue
			}
			for _, id := range ids {
				_, deliveryErr := e.Repo.DeliverReminder(ctx, id, func(ctx context.Context, a conversations.ApprovalAuthority) error {
					err := e.VerifyAuthority(ctx, Authority(a))
					if errors.Is(err, sql.ErrNoRows) || errors.Is(err, oauthstore.ErrInvalid) {
						return conversations.ErrReminderRevoked
					}
					return err
				})
				if deliveryErr != nil {
					continue
				} // Retry on the next bounded scheduler tick.
			}
		}
	}
}
