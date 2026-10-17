package conversations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
)

// ToolOperation records a durable tool invocation and its observed outcome.
type ToolOperation struct{ ID, Effect, State, Outcome string }

// PrepareTool never stores inputs. External effects must use a stable semantic
// operation ID, and commit Started before dispatch; no skip-dedup escape exists.
func (r *Repository) PrepareTool(ctx context.Context, f Fence, id, effect string, payload []byte) (ToolOperation, error) {
	if id == "" || len(id) > 200 || len(payload) > 1<<20 || (effect != "read" && effect != "mutating" && effect != "unknown") {
		return ToolOperation{}, ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return ToolOperation{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr1 := r.fence(ctx, tx, f); checkErr1 != nil {
		return ToolOperation{}, checkErr1
	}
	digest := sha256.Sum256(payload)
	var old []byte
	op := ToolOperation{ID: id, Effect: effect, State: "prepared"}
	err = tx.QueryRowContext(ctx, `SELECT digest,effect,state,outcome FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND id=?`, f.OwnerID, f.TurnID, id).Scan(&old, &op.Effect, &op.State, &op.Outcome)
	if err == nil {
		if !bytes.Equal(old, digest[:]) || op.Effect != effect {
			return op, ErrIdempotency
		}
		return op, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return op, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_tool_operations(owner_id,conversation_id,turn_id,id,digest,effect,state,processing_epoch) VALUES(?,?,?,?,?,?,'prepared',?)`, f.OwnerID, f.ConversationID, f.TurnID, id, digest[:], effect, f.ProcessingEpoch)
	if err != nil {
		return op, err
	}
	return op, tx.Commit()
}

// StartTool records tool dispatch before an external effect can begin.
func (r *Repository) StartTool(ctx context.Context, f Fence, id string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr2 := r.fence(ctx, tx, f); checkErr2 != nil {
		return checkErr2
	}
	res, err := tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state='started' WHERE owner_id=? AND turn_id=? AND id=? AND state='prepared' AND processing_epoch=?`, f.OwnerID, f.TurnID, id, f.ProcessingEpoch)
	if err != nil {
		return err
	}
	n, operationErr1 := res.RowsAffected()
	if operationErr1 != nil {
		return operationErr1
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// FinishTool records the tool outcome under the current execution fence.
func (r *Repository) FinishTool(ctx context.Context, f Fence, id, state, outcome string) error {
	if (state != "succeeded" && state != "failed" && state != "unknown_outcome") || len(outcome) > 64<<10 {
		return ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr3 := r.fence(ctx, tx, f); checkErr3 != nil {
		return checkErr3
	}
	// Native local mutations atomically commit the result with their effect.
	// Accept only that exact terminal result, never overwrite another outcome.
	var oldState, oldOutcome string
	if checkErr := tx.QueryRowContext(ctx, `SELECT state,outcome FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND id=? AND processing_epoch=?`, f.OwnerID, f.TurnID, id, f.ProcessingEpoch).Scan(&oldState, &oldOutcome); checkErr != nil {
		return checkErr
	}
	if state == "succeeded" && oldState == state && oldOutcome == outcome {
		return tx.Commit()
	}
	res, err := tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state=?,outcome=? WHERE owner_id=? AND turn_id=? AND id=? AND state='started' AND processing_epoch=?`, state, outcome, f.OwnerID, f.TurnID, id, f.ProcessingEpoch)
	if err != nil {
		return err
	}
	n, operationErr2 := res.RowsAffected()
	if operationErr2 != nil {
		return operationErr2
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

// RejectPreparedTool closes a denied pre-dispatch operation without recording a
// dispatched mutation. Repeated model calls cannot reopen its prompt.
func (r *Repository) RejectPreparedTool(ctx context.Context, f Fence, id string) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Preserve operation result.
	if fenceErr := r.fence(ctx, tx, f); fenceErr != nil {
		return fenceErr
	}
	res, err := tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state='failed' WHERE owner_id=? AND turn_id=? AND id=? AND state='prepared' AND processing_epoch=?`, f.OwnerID, f.TurnID, id, f.ProcessingEpoch)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}
