package conversations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
)

// ErrResync requires the caller to obtain a fresh snapshot or operation result.
var ErrResync = errors.New("mobile snapshot resynchronization required")

// Event identifies a durable owner-scoped state transition.
type Event struct {
	ID, ConversationID, TurnID, InputMessageID, Type, Cursor string
	Seq, Generation, TurnSeq                                 uint64
}

func (r *Repository) event(ctx context.Context, tx *repoTx, owner, conversation string, generation uint64, turn string, turnSeq uint64, input, kind string) error {
	var seq uint64
	if err := tx.QueryRowContext(ctx, `INSERT INTO mobile_event_sequences(owner_id,last_seq) VALUES(?,1) ON CONFLICT(owner_id) DO UPDATE SET last_seq=last_seq+1 RETURNING last_seq`, owner).Scan(&seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mobile_events(owner_id,event_seq,id,conversation_id,generation,turn_id,turn_seq,input_message_id,event_type,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, owner, seq, uuid.NewString(), conversation, generation, turn, turnSeq, input, kind, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM mobile_events WHERE owner_id=? AND (event_seq<=? OR created_at<?)`, owner, int64(seq)-10000, time.Now().Add(-7*24*time.Hour).UnixMilli())
	return err
}

// SnapshotItem contains one conversation entry in a paged snapshot.
type SnapshotItem struct {
	Conversation Conversation
	ActiveTurns  []Turn
	Prompts      []Prompt
	TaskIDs      []string
}

// Snapshot contains a bounded owner-scoped view and its event watermark.
type Snapshot struct {
	ID, Cursor, NextPage string
	Watermark            uint64
	Items                []SnapshotItem
	ExpiresAt            time.Time
}
type eventCursor struct {
	Owner, Subscription, Snapshot string
	Seq                           uint64
	Expires                       int64
}
type snapshotCursor struct {
	Owner, Subscription, Snapshot string
	Offset                        int
	Expires                       int64
}

func subscription(ids []string) (selection []string, digest string, returnErr error) {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	if len(ids) > 100 {
		return nil, "", ErrInvalid
	}
	for i, id := range ids {
		if !validID(id) || (i > 0 && ids[i-1] == id) {
			return nil, "", ErrInvalid
		}
	}
	data, _ := json.Marshal(ids) //nolint:errcheck // This internal snapshot contains only JSON-supported values.
	sum := sha256.Sum256(data)
	return ids, hex.EncodeToString(sum[:]), nil
}
func selected(ids []string, id string) bool {
	if len(ids) == 0 {
		return true
	}
	i := sort.SearchStrings(ids, id)
	return i < len(ids) && ids[i] == id
}

// Snapshot returns a bounded owner-scoped snapshot and event watermark.
func (r *Repository) Snapshot(ctx context.Context, owner string, ids []string, limit int, page string) (Snapshot, error) {
	if !validID(owner) || limit < 1 || limit > 100 {
		return Snapshot{}, ErrInvalid
	}
	ids, binding, err := subscription(ids)
	if err != nil {
		return Snapshot{}, err
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	now := time.Now()
	var all []SnapshotItem
	var out Snapshot
	offset := 0
	if page != "" {
		var p snapshotCursor
		if r.cursors.Open("snapshot.page.v1", page, &p) != nil || p.Owner != owner || p.Subscription != binding || p.Expires <= now.Unix() || p.Offset < 0 {
			return out, ErrResync
		}
		var body []byte
		var expiry int64
		if tx.QueryRowContext(ctx, `SELECT id,watermark,body,expires_at FROM mobile_snapshots WHERE id=? AND owner_id=? AND subscription=? AND expires_at>?`, p.Snapshot, owner, binding, now.Unix()).Scan(&out.ID, &out.Watermark, &body, &expiry) != nil || json.Unmarshal(body, &all) != nil {
			return out, ErrResync
		}
		out.ExpiresAt = time.Unix(expiry, 0)
		offset = p.Offset
	} else {
		for _, id := range ids {
			if _, err = get(ctx, tx, owner, id); err != nil {
				return out, err
			}
		}
		rows, stepErr2 := tx.QueryContext(ctx, `SELECT id FROM mobile_conversations WHERE owner_id=? ORDER BY id LIMIT 1001`, owner)
		if stepErr2 != nil {
			return out, stepErr2
		}
		var found []string
		defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
		for rows.Next() {
			var id string
			if stepErr2 = rows.Scan(&id); stepErr2 != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return out, stepErr2
			}
			if selected(ids, id) {
				found = append(found, id)
			}
		}
		stepErr2 = rows.Err()
		rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		if stepErr2 != nil {
			return out, stepErr2
		}
		if len(found) > 1000 {
			return out, ErrInvalid
		}
		for _, id := range found {
			c, stepErr3 := getFull(ctx, tx, owner, id)
			if stepErr3 != nil {
				return out, stepErr3
			}
			item := SnapshotItem{Conversation: c}
			turns, stepErr3 := tx.QueryContext(ctx, `SELECT id FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND generation=? AND state IN ('accepted','queued','running','awaiting_prompt') ORDER BY turn_seq`, owner, id, c.Generation)
			if stepErr3 != nil {
				return out, stepErr3
			}
			var keys []string
			defer turns.Close() //nolint:errcheck,gocritic // Each bounded iteration closes explicitly; this is an early-return fallback.
			for turns.Next() {
				var key string
				if stepErr3 = turns.Scan(&key); stepErr3 != nil {
					turns.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
					return out, stepErr3
				}
				keys = append(keys, key)
			}
			stepErr3 = turns.Err()
			turns.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			if stepErr3 != nil {
				return out, stepErr3
			}
			for _, key := range keys {
				t, e := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, owner, id, key))
				if e != nil {
					return out, e
				}
				item.ActiveTurns = append(item.ActiveTurns, t)
			}
			item.Prompts, stepErr3 = r.prompts(ctx, tx, owner, id, c.Generation)
			if stepErr3 != nil {
				return out, stepErr3
			}
			taskRows, stepErr3 := tx.QueryContext(ctx, `SELECT task_id FROM mobile_task_references WHERE owner_id=? AND conversation_id=? AND expires_at>? AND (state IN ('queued','running','awaiting_confirmation','needs_user_action') OR task_id IN (SELECT task_id FROM mobile_task_references recent WHERE recent.owner_id=? AND recent.conversation_id=? ORDER BY created_at DESC LIMIT 32)) ORDER BY (state IN ('queued','running','awaiting_confirmation','needs_user_action')) DESC,created_at DESC LIMIT 200`, owner, id, time.Now().UnixMilli(), owner, id)
			if stepErr3 != nil {
				return out, stepErr3
			}
			defer taskRows.Close() //nolint:errcheck,gocritic // Each bounded iteration closes explicitly; this is an early-return fallback.
			for taskRows.Next() {
				var taskID string
				if stepErr3 = taskRows.Scan(&taskID); stepErr3 != nil {
					taskRows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
					return out, stepErr3
				}
				item.TaskIDs = append(item.TaskIDs, taskID)
			}
			stepErr3 = taskRows.Err()
			taskRows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			if stepErr3 != nil {
				return out, stepErr3
			}
			all = append(all, item)
		}
		if checkErr6 := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(last_seq),0) FROM mobile_event_sequences WHERE owner_id=?`, owner).Scan(&out.Watermark); checkErr6 != nil {
			return out, checkErr6
		}
		out.ID = uuid.NewString()
		out.ExpiresAt = now.Add(15 * time.Minute).Truncate(time.Second)
		body, stepErr2 := json.Marshal(all)
		if len(body) > 4<<20 {
			return out, ErrQueueFull
		}
		if stepErr2 != nil {
			return out, stepErr2
		}
		if _, stepErr2 = tx.ExecContext(ctx, `DELETE FROM mobile_snapshots WHERE expires_at<=?`, now.Unix()); stepErr2 != nil {
			return out, stepErr2
		}
		var count int
		if checkErr7 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_snapshots WHERE owner_id=?`, owner).Scan(&count); checkErr7 != nil {
			return out, checkErr7
		}
		if count >= 32 {
			return out, ErrQueueFull
		}
		if _, stepErr2 = tx.ExecContext(ctx, `INSERT INTO mobile_snapshots VALUES(?,?,?,?,?,?)`, out.ID, owner, binding, out.Watermark, body, out.ExpiresAt.Unix()); stepErr2 != nil {
			return out, stepErr2
		}
	}
	if offset > len(all) {
		return out, ErrResync
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	out.Items = all[offset:end]
	out.Cursor, err = r.cursors.Seal("events.v1", eventCursor{owner, binding, out.ID, out.Watermark, out.ExpiresAt.Unix()})
	if err != nil {
		return out, err
	}
	if end < len(all) {
		out.NextPage, err = r.cursors.Seal("snapshot.page.v1", snapshotCursor{owner, binding, out.ID, end, out.ExpiresAt.Unix()})
		if err != nil {
			return out, err
		}
	}
	return out, tx.Commit()
}

// Events returns authorized events after the supplied sequence.
func (r *Repository) Events(ctx context.Context, owner string, ids []string, snapshot, cursor string, limit int) ([]Event, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", ErrInvalid
	}
	ids, binding, err := subscription(ids)
	if err != nil {
		return nil, "", err
	}
	var c eventCursor
	if r.cursors.Open("events.v1", cursor, &c) != nil || c.Owner != owner || c.Subscription != binding || c.Snapshot != snapshot || c.Expires <= time.Now().Unix() {
		return nil, "", ErrResync
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_snapshots WHERE owner_id=? AND id=? AND subscription=? AND expires_at>?`, owner, snapshot, binding, time.Now().Unix()).Scan(&exists); err != nil || exists != 1 {
		return nil, "", ErrResync
	}
	var minSequence, maxSequence uint64
	if checkErr4 := tx.QueryRowContext(ctx, `SELECT COALESCE(MIN(event_seq),0),COALESCE(MAX(event_seq),0) FROM mobile_events WHERE owner_id=?`, owner).Scan(&minSequence, &maxSequence); checkErr4 != nil {
		return nil, "", checkErr4
	}
	if minSequence > 0 && c.Seq < minSequence-1 {
		return nil, "", ErrResync
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_seq,conversation_id,generation,turn_id,turn_seq,input_message_id,event_type FROM mobile_events WHERE owner_id=? AND event_seq>? ORDER BY event_seq LIMIT ?`, owner, c.Seq, limit)
	if err != nil {
		return nil, "", err
	}
	var out []Event
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.ID, &e.Seq, &e.ConversationID, &e.Generation, &e.TurnID, &e.TurnSeq, &e.InputMessageID, &e.Type); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, "", err
		}
		c.Seq = e.Seq
		if selected(ids, e.ConversationID) {
			e.Cursor, err = r.cursors.Seal("events.v1", c)
			if err != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return nil, "", err
			}
			out = append(out, e)
		}
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, "", err
	}
	newCursor, err := r.cursors.Seal("events.v1", c)
	if err != nil {
		return nil, "", err
	}
	return out, newCursor, tx.Commit()
}
