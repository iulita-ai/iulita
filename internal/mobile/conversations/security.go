package conversations

import (
	"context"
	"time"
)

// ReconcileSecurity makes account invalidation durable in business state, not
// just context cancellation. Otherwise an old-stamp running turn could retain
// an owner queue slot forever after every late commit is correctly fenced.
func (r *Repository) ReconcileSecurity(ctx context.Context) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	rows, err := tx.QueryContext(ctx, `SELECT owner_id,conversation_id,id,generation,turn_seq,input_message_id,account_stamp FROM mobile_turns WHERE state IN ('accepted','queued','running','awaiting_prompt') LIMIT 1000`)
	if err != nil {
		return err
	}
	type staleTurn struct {
		Turn
		stamp string
	}
	var turns []staleTurn
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var t staleTurn
		if err = rows.Scan(&t.OwnerID, &t.ConversationID, &t.ID, &t.Generation, &t.TurnSeq, &t.InputMessageID, &t.stamp); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return err
		}
		turns = append(turns, t)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return err
	}
	for i := range turns {
		t := &turns[i]
		current, e := accountStamp(ctx, tx, t.OwnerID)
		if e == nil && current == t.stamp {
			continue
		}
		var effects int
		if checkErr1 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND (state IN ('started','unknown_outcome') OR (state='succeeded' AND effect<>'read'))`, t.OwnerID, t.ID).Scan(&effects); checkErr1 != nil {
			return checkErr1
		}
		state := "cancelled"
		if effects > 0 {
			state = "unknown_outcome"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state=?,outcome_reason='security_invalidated',cancellation_requested=1,cancelled_at=?,processing_epoch=processing_epoch+1 WHERE owner_id=? AND id=?`, state, time.Now().UnixMilli(), t.OwnerID, t.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='invalidated' WHERE owner_id=? AND turn_id=? AND state='open'`, t.OwnerID, t.ID); err != nil {
			return err
		}
		if checkErr2 := r.event(ctx, tx, t.OwnerID, t.ConversationID, t.Generation, t.ID, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr2 != nil {
			return checkErr2
		}
	}
	return tx.Commit()
}
