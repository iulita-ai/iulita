package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/security"
)

func dayResourceKey(owner string, item dayview.Item) string {
	return dayview.Revision(owner, item.Source, item.Connection, item.ListID, item.ID)
}

// ResolveDayOperation records owner acknowledgement without retrying or asserting remote success.
func (s *Store) ResolveDayOperation(ctx context.Context, owner, conversation, id, expected string) error {
	if owner == "" || conversation == "" || (expected != "unknown_outcome" && expected != "inconclusive" && expected != "not_observed" && expected != "revoked") {
		return fmt.Errorf("invalid operation resolution")
	}
	res, e := s.db.NewUpdate().Model((*dayview.Operation)(nil)).Set("status = ?", "acknowledged_by_user").Set("last_checked_at = ?", time.Now()).Where("id = ? AND owner = ? AND conversation = ? AND status = ?", id, owner, conversation, expected).Exec(ctx)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return dayview.ErrStale
	}
	return nil
}

// DayOperations exposes scoped uncertain-operation receipts as read-only day items.
type DayOperations struct {
	Store *Store
	Chat  string
}

// Name returns the uncertain-operation source name.
func (DayOperations) Name() string { return "operations" }

// List lists scoped reconciliation receipts without exposing raw provider errors.
func (d DayOperations) List(ctx context.Context, owner string, _ time.Time, _ *time.Location) ([]dayview.Item, error) {
	ops, e := d.Store.ListDayOperations(ctx, owner, d.Chat)
	if e != nil {
		return nil, e
	}
	var out []dayview.Item
	for index := range ops {
		op := &ops[index]
		resolvable := op.Status == "unknown_outcome" || op.Status == "inconclusive" || op.Status == "not_observed" || op.Status == "revoked"
		title := "Operation"
		var command dayview.Command
		if json.Unmarshal([]byte(op.CommandJSON), &command) == nil {
			name := command.Item.Title
			if strings.TrimSpace(name) == "" {
				name = command.Item.Source + " #" + command.Item.ID
			}
			safe := []rune(security.RedactSensitiveText(command.Action + " · " + name))
			if len(safe) > 300 {
				safe = safe[:300]
			}
			title = string(safe)
		}
		out = append(out, dayview.Item{ID: op.ID, Source: "operations", Title: title + " · " + op.Status, Revision: dayview.Revision(op.ID, op.Status), ResolveDelivery: resolvable})
	}
	return out, nil
}

// Change records explicit uncertainty acknowledgement without replaying a write.
func (d DayOperations) Change(ctx context.Context, owner string, item dayview.Item, action string, _ time.Time) error {
	if action != "acknowledge" || !item.ResolveDelivery {
		return dayview.ErrUnavailable
	}
	ops, e := d.Store.ListDayOperations(ctx, owner, d.Chat)
	if e != nil {
		return e
	}
	for index := range ops {
		op := &ops[index]
		if op.ID == item.ID {
			if dayview.Revision(op.ID, op.Status) != item.Revision {
				return dayview.ErrStale
			}
			return d.Store.ResolveDayOperation(ctx, owner, d.Chat, op.ID, op.Status)
		}
	}
	return dayview.ErrUnavailable
}

// Verify observes acknowledgement status without changing a receipt.
func (d DayOperations) Verify(ctx context.Context, owner string, item dayview.Item, action string, _ time.Time) (dayview.Verification, error) {
	if action != "acknowledge" || owner == "" {
		return dayview.Inconclusive, nil
	}
	var count int
	e := d.Store.db.NewRaw("SELECT COUNT(*) FROM operations WHERE id = ? AND owner = ? AND conversation = ? AND status = ?", item.ID, owner, d.Chat, "acknowledged_by_user").Scan(ctx, &count)
	if e != nil {
		return dayview.Inconclusive, e
	}
	if count == 1 {
		return dayview.EffectObserved, nil
	}
	return dayview.NotObserved, nil
}
