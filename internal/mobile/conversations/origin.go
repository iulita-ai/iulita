package conversations

import (
	"context"
	"time"
)

// OriginAllowed is a read-only predicate for an already accepted asynchronous
// phone task. Its caller holds the account write reservation while committing
// the task DB transition; do not begin a second account write transaction here.
func (r *Repository) OriginAllowed(ctx context.Context, owner, conversation, turn string, generation uint64) error {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_turns t JOIN mobile_conversations c ON c.owner_id=t.owner_id AND c.id=t.conversation_id JOIN users u ON u.id=t.owner_id WHERE t.owner_id=? AND t.conversation_id=? AND t.id=? AND t.generation=? AND c.generation=t.generation AND t.cancellation_requested=0 AND t.state NOT IN ('failed','cancelled','unknown_outcome') AND u.disabled=0 AND u.must_change_pass=0`, owner, conversation, turn, generation).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrFenced
	}
	current, err := accountStamp(ctx, r.db, owner)
	if err != nil {
		return ErrFenced
	}
	var accepted string
	if r.db.QueryRowContext(ctx, `SELECT account_stamp FROM mobile_turns WHERE owner_id=? AND id=?`, owner, turn).Scan(&accepted) != nil || accepted != current {
		return ErrFenced
	}
	return nil
}

// DispatchAllowed checks that the originating turn still permits dispatch.
func (r *Repository) DispatchAllowed(ctx context.Context, f Fence) error {
	if err := r.OriginAllowed(ctx, f.OwnerID, f.ConversationID, f.TurnID, f.Generation); err != nil {
		return err
	}
	var accepted string
	err := r.db.QueryRowContext(ctx, `SELECT t.account_stamp FROM mobile_turns t JOIN mobile_runtime rt ON rt.id=1 WHERE t.owner_id=? AND t.id=? AND t.processing_epoch=? AND rt.instance=? AND rt.epoch=? AND rt.expires_at>?`, f.OwnerID, f.TurnID, f.ProcessingEpoch, f.Runtime.Instance, f.Runtime.Epoch, time.Now().UnixMilli()).Scan(&accepted)
	if err != nil {
		return ErrFenced
	}
	current, err := accountStamp(ctx, r.db, f.OwnerID)
	if err != nil || current != accepted {
		return ErrFenced
	}
	return nil
}
