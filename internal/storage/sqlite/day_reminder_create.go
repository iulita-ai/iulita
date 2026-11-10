package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/uptrace/bun"
)

type dayReminderCreation struct {
	ID            string `bun:",pk"`
	Owner         string
	Conversation  string
	ReminderID    int64
	CommandDigest string
}

// CreateDayReminder commits an idempotency record and a single reminder in one
// transaction. Call this instead of the generic ClaimDayOperation path. The
// operationID is a server-created key bound to the confirmed exact command.
func (s *Store) CreateDayReminder(ctx context.Context, owner, conversation, title, zone string, due time.Time, operationID string) (id int64, created bool, err error) {
	if owner == "" || conversation == "" || operationID == "" || strings.TrimSpace(title) == "" || !due.After(time.Now()) {
		return 0, false, fmt.Errorf("invalid reminder command")
	}
	if _, e := time.LoadLocation(zone); e != nil {
		return 0, false, fmt.Errorf("invalid timezone")
	}
	route, e := s.GetTelegramRoute(ctx, conversation)
	if e != nil || route == nil || route.OwnerID != owner || !route.Private {
		return 0, false, fmt.Errorf("private owned route required")
	}
	err = s.runWriteTransaction(ctx, func(ctx context.Context, tx bun.IDB) error {
		if e := conversationAvailable(ctx, tx, conversation); e != nil {
			return e
		}
		claim := &dayReminderCreation{ID: operationID, Owner: owner, Conversation: conversation, CommandDigest: dayview.Revision(owner, conversation, title, zone, due.UTC().Format(time.RFC3339Nano))}
		res, e := tx.NewInsert().Model(claim).On("CONFLICT (id) DO NOTHING").Exec(ctx)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			old := new(dayReminderCreation)
			if readErr := tx.NewSelect().Model(old).Where("id = ?", operationID).Scan(ctx); readErr != nil {
				return readErr
			}
			if old.Owner != owner || old.Conversation != conversation || old.CommandDigest != claim.CommandDigest {
				return fmt.Errorf("operation ownership mismatch")
			}
			id = old.ReminderID
			return nil
		}
		r := &domain.Reminder{ChatID: conversation, UserID: owner, Title: security.RedactSensitiveText(title), DueAt: due.UTC(), Timezone: zone, Status: "pending", CreatedAt: time.Now()}
		if _, e = tx.NewInsert().Model(r).Exec(ctx); e != nil {
			return e
		}
		if _, e = tx.NewUpdate().Model((*dayReminderCreation)(nil)).Set("reminder_id = ?", r.ID).Where("id = ?", operationID).Exec(ctx); e != nil {
			return e
		}
		id = r.ID
		created = true
		return nil
	})
	return
}
