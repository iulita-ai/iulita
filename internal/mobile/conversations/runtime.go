package conversations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrFenced rejects stale execution authority.
var ErrFenced = errors.New("mobile turn is no longer executable")

// ErrBusy indicates that current capacity or concurrency limits were reached.
var ErrBusy = errors.New("mobile runtime already has a live coordinator")

func accountStamp(ctx context.Context, tx queryer, owner string) (string, error) {
	var hash string
	var disabled, mustChange bool
	var epoch uint64
	if err := tx.QueryRowContext(ctx, `SELECT password_hash,disabled,must_change_pass,security_epoch FROM users WHERE id=?`, owner).Scan(&hash, &disabled, &mustChange, &epoch); err != nil {
		return "", err
	}
	if disabled || mustChange {
		return "", ErrFenced
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", hash, epoch)))
	return hex.EncodeToString(sum[:]), nil
}

// Runtime owns the mobile execution lifecycle and its fencing state.
type Runtime struct {
	Instance string
	Epoch    uint64
}

// Fence binds execution to the current runtime and turn generations.
type Fence struct {
	OwnerID, ConversationID, TurnID string
	Generation, ProcessingEpoch     uint64
	Runtime                         Runtime
}

// AcquireRuntime is a single-replica lease. A second process cannot take over a
// live coordinator; after expiry, started work becomes unknown, never replayed.
func (r *Repository) AcquireRuntime(ctx context.Context) (Runtime, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	var epoch uint64
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT epoch,expires_at FROM mobile_runtime WHERE id=1`).Scan(&epoch, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, err
	}
	now := time.Now().UnixMilli()
	if expires > now {
		return Runtime{}, ErrBusy
	}
	runtime := Runtime{uuid.NewString(), epoch + 1}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_runtime(id,instance,epoch,expires_at) VALUES(1,?,?,?) ON CONFLICT(id) DO UPDATE SET instance=excluded.instance,epoch=excluded.epoch,expires_at=excluded.expires_at`, runtime.Instance, runtime.Epoch, now+10000); err != nil {
		return runtime, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT owner_id,conversation_id,id,generation,turn_seq,input_message_id FROM mobile_turns WHERE state IN ('running','awaiting_prompt')`)
	if err != nil {
		return runtime, err
	}
	var abandoned []Turn
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var t Turn
		if err = rows.Scan(&t.OwnerID, &t.ConversationID, &t.ID, &t.Generation, &t.TurnSeq, &t.InputMessageID); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return runtime, err
		}
		abandoned = append(abandoned, t)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return runtime, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state='unknown_outcome',outcome_reason='coordinator_lost',processing_epoch=processing_epoch+1 WHERE state IN ('running','awaiting_prompt')`); err != nil {
		return runtime, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state='unknown_outcome',outcome='' WHERE state='started'`); err != nil {
		return runtime, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='invalidated' WHERE state='open'`); err != nil {
		return runtime, err
	}
	for i := range abandoned {
		t := &abandoned[i]
		if checkErr1 := r.event(ctx, tx, t.OwnerID, t.ConversationID, t.Generation, t.ID, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr1 != nil {
			return runtime, checkErr1
		}
	}
	return runtime, tx.Commit()
}

// RenewRuntime renews the runtime lease only for its current generation.
func (r *Repository) RenewRuntime(ctx context.Context, rt Runtime) error {
	res, err := r.db.ExecContext(ctx, `UPDATE mobile_runtime SET expires_at=? WHERE id=1 AND instance=? AND epoch=? AND expires_at>?`, time.Now().Add(10*time.Second).UnixMilli(), rt.Instance, rt.Epoch, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, operationErr1 := res.RowsAffected()
	if operationErr1 != nil {
		return operationErr1
	}
	if n != 1 {
		return ErrFenced
	}
	return nil
}

// ReleaseRuntime releases the current runtime lease.
func (r *Repository) ReleaseRuntime(ctx context.Context, rt Runtime) error {
	_, err := r.db.ExecContext(ctx, `UPDATE mobile_runtime SET expires_at=0 WHERE id=1 AND instance=? AND epoch=?`, rt.Instance, rt.Epoch)
	return err
}
func checkRuntime(ctx context.Context, tx *repoTx, rt Runtime) error {
	var n int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_runtime WHERE id=1 AND instance=? AND epoch=? AND expires_at>?`, rt.Instance, rt.Epoch, time.Now().UnixMilli()).Scan(&n) != nil || n != 1 {
		return ErrFenced
	}
	return nil
}

// Claim claims an accepted turn under the runtime lease.
func (r *Repository) Claim(ctx context.Context, rt Runtime, owner string) (Turn, Fence, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, Fence{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr2 := checkRuntime(ctx, tx, rt); checkErr2 != nil {
		return Turn{}, Fence{}, checkErr2
	}
	root := &repoRequest{repository: r, tx: tx.Tx}
	defer func() { root.done = true }()
	t, err := r.ClaimNext(context.WithValue(ctx, transactionKey{}, root), owner)
	if err != nil {
		return t, Fence{}, err
	}
	var epoch uint64
	if checkErr3 := tx.QueryRowContext(ctx, `UPDATE mobile_turns SET processing_epoch=processing_epoch+1 WHERE owner_id=? AND conversation_id=? AND id=? RETURNING processing_epoch`, owner, t.ConversationID, t.ID).Scan(&epoch); checkErr3 != nil {
		return t, Fence{}, checkErr3
	}
	f := Fence{owner, t.ConversationID, t.ID, t.Generation, epoch, rt}
	if checkErr4 := r.event(ctx, tx, owner, t.ConversationID, t.Generation, t.ID, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr4 != nil {
		return t, f, checkErr4
	}
	return t, f, tx.Commit()
}
func (r *Repository) fence(ctx context.Context, tx *repoTx, f Fence) error {
	if err := checkRuntime(ctx, tx, f.Runtime); err != nil {
		return err
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_turns t JOIN mobile_conversations c ON c.owner_id=t.owner_id AND c.id=t.conversation_id JOIN users u ON u.id=t.owner_id WHERE t.owner_id=? AND t.conversation_id=? AND t.id=? AND t.generation=? AND c.generation=t.generation AND t.processing_epoch=? AND t.state IN ('running','awaiting_prompt') AND t.cancellation_requested=0 AND u.disabled=0`, f.OwnerID, f.ConversationID, f.TurnID, f.Generation, f.ProcessingEpoch).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrFenced
	}
	current, err := accountStamp(ctx, tx, f.OwnerID)
	if err != nil {
		return ErrFenced
	}
	var accepted string
	if tx.QueryRowContext(ctx, `SELECT account_stamp FROM mobile_turns WHERE owner_id=? AND id=?`, f.OwnerID, f.TurnID).Scan(&accepted) != nil || accepted != current {
		return ErrFenced
	}
	return nil
}

// CheckFence verifies runtime and turn authority before an effect.
func (r *Repository) CheckFence(ctx context.Context, f Fence) error {
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr5 := r.fence(ctx, tx, f); checkErr5 != nil {
		return checkErr5
	}
	return tx.Commit()
}

// TurnContext roots timeout extensions in business cancellation, independent of
// an RPC. Polling complements transactional fences before every durable effect.
func (r *Repository) TurnContext(parent context.Context, f Fence) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if r.CheckFence(ctx, f) != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// Cancel records cancellation intent and invalidates further task authority.
func (r *Repository) Cancel(ctx context.Context, owner, conversation, turn string, generation uint64) (Turn, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return Turn{}, err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	c, err := get(ctx, tx, owner, conversation)
	if err != nil {
		return Turn{}, err
	}
	if c.Generation != generation {
		return Turn{}, ErrConflict
	}
	t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=? AND generation=?`, owner, conversation, turn, generation))
	if err != nil {
		return t, err
	}
	if t.State == "queued" || t.State == "accepted" || t.State == "running" || t.State == "awaiting_prompt" {
		// A dispatched tool may already have changed the outside world.
		var started int
		if checkErr6 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND (state IN ('started','unknown_outcome') OR (state='succeeded' AND effect<>'read'))`, owner, turn).Scan(&started); checkErr6 != nil {
			return t, checkErr6
		}
		state := "cancelled"
		if started > 0 {
			state = "unknown_outcome"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET cancellation_requested=1,cancelled_at=?,state=?,processing_epoch=processing_epoch+1 WHERE owner_id=? AND conversation_id=? AND id=?`, time.Now().UnixMilli(), state, owner, conversation, turn); err != nil {
			return t, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='invalidated' WHERE owner_id=? AND turn_id=? AND state='open'`, owner, turn); err != nil {
			return t, err
		}
		if checkErr7 := r.event(ctx, tx, owner, conversation, generation, turn, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr7 != nil {
			return t, checkErr7
		}
		t, err = scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, owner, conversation, turn))
		if err != nil {
			return t, err
		}
	} else if !t.CancellationRequested {
		// A completed chat turn may still own an asynchronous phone task.
		// Preserve the truthful terminal outcome while revoking its business root.
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET cancellation_requested=1,cancelled_at=?,processing_epoch=processing_epoch+1 WHERE owner_id=? AND conversation_id=? AND id=?`, time.Now().UnixMilli(), owner, conversation, turn); err != nil {
			return t, err
		}
		if checkErr8 := r.event(ctx, tx, owner, conversation, generation, turn, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr8 != nil {
			return t, checkErr8
		}
		t, err = scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, owner, conversation, turn))
		if err != nil {
			return t, err
		}
	}
	return t, tx.Commit()
}

// Complete finalizes the turn while preserving unresolved-effect uncertainty.
func (r *Repository) Complete(ctx context.Context, f Fence, text, state string) error {
	if !utf8.ValidString(text) || len(text) > 1<<20 || (state != "completed" && state != "failed" && state != "unknown_outcome") {
		return ErrInvalid
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr9 := r.fence(ctx, tx, f); checkErr9 != nil {
		return checkErr9
	}
	t, err := scanTurn(tx.QueryRowContext(ctx, `SELECT `+turnColumns+` FROM mobile_turns WHERE owner_id=? AND conversation_id=? AND id=?`, f.OwnerID, f.ConversationID, f.TurnID))
	if err != nil {
		return err
	}
	if state != "unknown_outcome" {
		var unfinished int
		if checkErr10 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND state IN ('started','unknown_outcome')`, f.OwnerID, f.TurnID).Scan(&unfinished); checkErr10 != nil {
			return checkErr10
		}
		if unfinished > 0 {
			state = "unknown_outcome"
			text = ""
		}
	}
	if text != "" && state == "completed" {
		var bytes int64
		if checkErr11 := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM mobile_messages WHERE owner_id=?`, f.OwnerID).Scan(&bytes); checkErr11 != nil {
			return checkErr11
		}
		if bytes+int64(len(text)) > 64<<20 {
			return ErrQueueFull
		}
		var seq uint64
		if checkErr12 := tx.QueryRowContext(ctx, `UPDATE mobile_conversations SET message_seq=message_seq+1,updated_at=? WHERE owner_id=? AND id=? AND generation=? RETURNING message_seq`, time.Now().UnixMilli(), f.OwnerID, f.ConversationID, f.Generation).Scan(&seq); checkErr12 != nil {
			return checkErr12
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mobile_messages(id,owner_id,conversation_id,generation,turn_id,turn_seq,message_seq,role,text,created_at) VALUES(?,?,?,?,?,?,?,'assistant',?,?)`, uuid.NewString(), f.OwnerID, f.ConversationID, f.Generation, f.TurnID, t.TurnSeq, seq, text, time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_prompts SET state='invalidated' WHERE owner_id=? AND turn_id=? AND state='open'`, f.OwnerID, f.TurnID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_turns SET state=? WHERE owner_id=? AND conversation_id=? AND id=?`, state, f.OwnerID, f.ConversationID, f.TurnID); err != nil {
		return err
	}
	if checkErr13 := r.event(ctx, tx, f.OwnerID, f.ConversationID, f.Generation, f.TurnID, t.TurnSeq, t.InputMessageID, "turn.changed"); checkErr13 != nil {
		return checkErr13
	}
	if checkErr14 := bump(ctx, tx, f.OwnerID); checkErr14 != nil {
		return checkErr14
	}
	return tx.Commit()
}

// ContextFor contains only completed predecessors plus the exact current input.
// Accepted future inputs never become history for an earlier turn.
func (r *Repository) ContextFor(ctx context.Context, f Fence) ([]Message, string, error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
	if checkErr15 := r.fence(ctx, tx, f); checkErr15 != nil {
		return nil, "", checkErr15
	}
	var seq uint64
	var input string
	err = tx.QueryRowContext(ctx, `SELECT t.turn_seq,m.text FROM mobile_turns t JOIN mobile_messages m ON m.id=t.input_message_id AND m.owner_id=t.owner_id AND m.conversation_id=t.conversation_id WHERE t.owner_id=? AND t.conversation_id=? AND t.id=? AND m.generation=?`, f.OwnerID, f.ConversationID, f.TurnID, f.Generation).Scan(&seq, &input)
	if err != nil {
		return nil, "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.turn_id,m.turn_seq,m.message_seq,m.role,m.text,m.created_at FROM mobile_messages m JOIN mobile_turns t ON t.id=m.turn_id AND t.owner_id=m.owner_id AND t.conversation_id=m.conversation_id WHERE m.owner_id=? AND m.conversation_id=? AND m.generation=? AND m.turn_seq<? AND t.state='completed' ORDER BY m.turn_seq DESC,m.message_seq DESC LIMIT 100`, f.OwnerID, f.ConversationID, f.Generation, seq)
	if err != nil {
		return nil, "", err
	}
	var history []Message
	historyBytes := 0
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var m Message
		var created int64
		if err = rows.Scan(&m.ID, &m.TurnID, &m.TurnSeq, &m.MessageSeq, &m.Role, &m.Text, &created); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return nil, "", err
		}
		m.ConversationID = f.ConversationID
		m.Generation = f.Generation
		m.CreatedAt = time.UnixMilli(created)
		if historyBytes+len(m.Text) > 1<<20 {
			// Never retain the assistant half of a truncated predecessor pair.
			if len(history) > 0 && history[len(history)-1].TurnID == m.TurnID {
				history = history[:len(history)-1]
			}
			break
		}
		historyBytes += len(m.Text)
		history = append(history, m)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return nil, "", err
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	// The row limit can cut the oldest pair even when the byte limit did not.
	for len(history) > 0 && history[0].Role != "user" {
		history = history[1:]
	}
	return history, input, tx.Commit()
}

// PendingOwners returns owners with work awaiting execution.
func (r *Repository) PendingOwners(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT t.owner_id FROM mobile_turns t JOIN users u ON u.id=t.owner_id WHERE t.state IN ('accepted','queued') AND u.disabled=0 ORDER BY t.owner_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // Read-only result cleanup; rows.Err below is returned.
	var owners []string
	for rows.Next() {
		var owner string
		if checkErr16 := rows.Scan(&owner); checkErr16 != nil {
			return nil, checkErr16
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}
