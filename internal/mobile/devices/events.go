package devices

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
)

func taskEvent(ctx context.Context, tx *sql.Tx, task string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mobile_phone_events(id,owner_id,conversation_id,turn_id,history_generation,task_id,state,created_at) SELECT ?,owner_id,conversation_id,turn_id,history_generation,id,state,? FROM mobile_phone_tasks WHERE id=?`, uuid.NewString(), time.Now().UnixMilli(), task)
	return err
}
func taskEventForCommand(ctx context.Context, tx *sql.Tx, owner, device, id string) error {
	var task string
	if err := tx.QueryRowContext(ctx, `SELECT task_id FROM mobile_phone_commands WHERE owner_id=? AND device_id=? AND id=?`, owner, device, id).Scan(&task); err != nil {
		return err
	}
	return taskEvent(ctx, tx, task)
}
func taskTransitions(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query+` RETURNING id`, args...)
	if err != nil {
		return err
	}
	var tasks []string
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var task string
		if err = rows.Scan(&task); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return err
		}
		tasks = append(tasks, task)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if checkErr1 := taskEvent(ctx, tx, task); checkErr1 != nil {
			return checkErr1
		}
	}
	return nil
}

// PublishEvents publishes durable task metadata to its originating conversation.
func (s *Store) PublishEvents(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.owner_id,e.conversation_id,e.turn_id,e.history_generation,e.task_id,t.state,e.seq,e.created_at FROM mobile_phone_events e JOIN mobile_phone_tasks t ON t.id=e.task_id AND t.owner_id=e.owner_id WHERE e.published=0 ORDER BY e.seq LIMIT 100`)
	if err != nil {
		return err
	}
	var events []conversations.TaskEvent
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var e conversations.TaskEvent
		var created int64
		if err = rows.Scan(&e.ID, &e.Owner, &e.Conversation, &e.Turn, &e.Generation, &e.Task, &e.State, &e.SourceSeq, &created); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return err
		}
		e.CreatedAt = time.UnixMilli(created)
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return err
	}
	for i := range events {
		e := &events[i]
		if err = s.chats.PublishTask(ctx, *e); err != nil && !errors.Is(err, conversations.ErrNotFound) {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE mobile_phone_events SET published=1 WHERE id=?`, e.ID); err != nil {
			return err
		}
	}
	return nil
}
