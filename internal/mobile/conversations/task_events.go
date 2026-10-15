package conversations

import (
	"context"
	"time"
)

// TaskEvent carries device-task metadata into the originating conversation.
type TaskEvent struct {
	ID, Owner, Conversation, Turn, Task, State string
	Generation, SourceSeq                      uint64
	CreatedAt                                  time.Time
}

// PublishTask is an idempotent inbox for the OAuth/task DB metadata outbox. No
// body/summary is accepted. A lost source ACK may repeat this call safely.
func (r *Repository) PublishTask(ctx context.Context, e TaskEvent) error {
	if !validID(e.ID) || !validID(e.Task) || !validID(e.Turn) || e.SourceSeq == 0 {
		return ErrInvalid
	}
	if e.CreatedAt.Before(time.Now().Add(-7 * 24 * time.Hour)) {
		return nil
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if _, err = get(ctx, tx, e.Owner, e.Conversation); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO mobile_task_event_inbox(id,owner_id,expires_at) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING`, e.ID, e.Owner, e.CreatedAt.Add(7*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	n, operationErr1 := result.RowsAffected()
	if operationErr1 != nil {
		return operationErr1
	}
	if n == 0 {
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_task_references(owner_id,conversation_id,turn_id,task_id,state,source_seq,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET state=excluded.state,source_seq=excluded.source_seq,expires_at=excluded.expires_at WHERE excluded.source_seq>=mobile_task_references.source_seq`, e.Owner, e.Conversation, e.Turn, e.Task, e.State, e.SourceSeq, e.CreatedAt.UnixMilli(), e.CreatedAt.Add(7*24*time.Hour).UnixMilli())
	if err != nil {
		return err
	}
	if checkErr1 := r.event(ctx, tx, e.Owner, e.Conversation, e.Generation, e.Turn, 0, e.Task, "task.changed"); checkErr1 != nil {
		return checkErr1
	}
	for _, q := range []string{`DELETE FROM mobile_task_event_inbox WHERE expires_at<=?`, `DELETE FROM mobile_task_references WHERE expires_at<=?`} {
		if _, err = tx.ExecContext(ctx, q, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
