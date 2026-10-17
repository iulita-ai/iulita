package conversations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/defaults"
)

// ErrReminderRevoked permanently cancels a schedule after verified revocation.
var ErrReminderRevoked = errors.New("reminder authority permanently revoked")

// ReminderInput is identity-free: ownership and delivery chat come from Fence.
type ReminderInput struct {
	Action   string `json:"action"`
	Title    string `json:"title"`
	DueAt    string `json:"due_at"`
	Timezone string `json:"timezone"`
	ID       int64  `json:"id"`
	AfterID  int64  `json:"after_id"`
}

// Reminder is a bounded account-owned schedule summary.
type Reminder struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	DueAt          string `json:"due_at"`
	ConversationID string `json:"conversation_id"`
}

// ReminderTime parses a reminder date using its explicit IANA timezone, or the
// application fallback when absent. An RFC3339 offset always takes precedence.
func ReminderTime(in ReminderInput) (time.Time, error) {
	in.Timezone = defaults.TimezoneOrDefault(in.Timezone)
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	due, err := time.Parse(time.RFC3339, in.DueAt)
	if err != nil {
		due, err = time.ParseInLocation("2006-01-02T15:04:05", in.DueAt, loc)
	}
	if err != nil {
		return due, ErrInvalid
	}
	return due.UTC(), nil
}

// Reminders commits local scheduling/cancellation and its tool result together.
func (r *Repository) Reminders(ctx context.Context, f Fence, operation string, in ReminderInput, a ApprovalAuthority) (string, error) {
	if operation == "" || a.OwnerID != f.OwnerID || a.PrincipalType == "service" || !validID(a.FamilyID) || !validID(a.DeviceID) {
		return "", ErrApproval
	}
	var due time.Time
	var err error

	tx, err := r.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // Preserve operation result.
	if checkErr := r.fence(ctx, tx, f); checkErr != nil {
		return "", checkErr
	}
	var state, outcome string
	if checkErr := tx.QueryRowContext(ctx, `SELECT state,outcome FROM mobile_tool_operations WHERE owner_id=? AND turn_id=? AND id=? AND processing_epoch=?`, f.OwnerID, f.TurnID, operation, f.ProcessingEpoch).Scan(&state, &outcome); checkErr != nil {
		return "", checkErr
	}
	if state == "succeeded" {
		return outcome, tx.Commit()
	}
	if state != "started" {
		return "", ErrConflict
	}
	if in.Action == "create" {
		due, err = ReminderTime(in)
		if err != nil || !due.After(time.Now()) || due.After(time.Now().AddDate(5, 0, 0)) || strings.TrimSpace(in.Title) == "" || len(in.Title) > 4096 || !utf8.ValidString(in.Title) {
			return "", ErrInvalid
		}
	}
	var result any
	switch in.Action {
	case "create":
		var count int
		if checkErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_reminders WHERE owner_id=? AND state='pending'`, f.OwnerID).Scan(&count); checkErr != nil {
			return "", checkErr
		}
		if count >= 1000 {
			return "", ErrQueueFull
		}
		authority, marshalErr := json.Marshal(a)
		if marshalErr != nil {
			return "", marshalErr
		}
		var id int64
		if checkErr := tx.QueryRowContext(ctx, `INSERT INTO mobile_reminders(owner_id,conversation_id,generation,origin_turn,operation_id,title,due_at,state,authority,delivery_id) VALUES(?,?,?,?,?,?,?,'pending',?,?) RETURNING id`, f.OwnerID, f.ConversationID, f.Generation, f.TurnID, operation, in.Title, due.UnixMilli(), authority, uuid.NewString()).Scan(&id); checkErr != nil {
			return "", checkErr
		}
		result = struct {
			Status string `json:"status"`
			ID     int64  `json:"id"`
			DueAt  string `json:"due_at"`
		}{"scheduled", id, due.Format(time.RFC3339)}
	case "list":
		if in.AfterID < 0 {
			return "", ErrInvalid
		}
		rows, queryErr := tx.QueryContext(ctx, `SELECT r.id,r.title,r.due_at,r.conversation_id FROM mobile_reminders r JOIN mobile_conversations c ON c.id=r.conversation_id AND c.owner_id=r.owner_id AND c.generation=r.generation WHERE r.owner_id=? AND r.id>? AND r.state='pending' ORDER BY r.id LIMIT 11`, f.OwnerID, in.AfterID)
		if queryErr != nil {
			return "", queryErr
		}
		defer rows.Close() //nolint:errcheck // Fallback read cleanup; explicit close below precedes writes.
		items := []Reminder{}
		for rows.Next() {
			var x Reminder
			var ms int64
			if err = rows.Scan(&x.ID, &x.Title, &ms, &x.ConversationID); err != nil {
				closeErr := rows.Close()
				return "", errors.Join(err, closeErr)
			}
			x.DueAt = time.UnixMilli(ms).UTC().Format(time.RFC3339)
			items = append(items, x)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return "", err
		}
		more := len(items) > 10
		if more {
			items = items[:10]
		}
		var next int64
		if more {
			next = items[len(items)-1].ID
		}
		result = struct {
			Reminders   []Reminder `json:"reminders"`
			More        bool       `json:"more"`
			NextAfterID int64      `json:"next_after_id,omitempty"`
		}{items, more, next}
	case "delete":
		res, execErr := tx.ExecContext(ctx, `UPDATE mobile_reminders SET state='cancelled',title='',authority='{}' WHERE owner_id=? AND id=? AND state='pending'`, f.OwnerID, in.ID)
		if execErr != nil {
			return "", execErr
		}
		n, execErr := res.RowsAffected()
		if execErr != nil {
			return "", execErr
		}
		if n != 1 {
			return "", ErrNotFound
		}
		result = struct {
			Status string `json:"status"`
		}{"cancelled"}
	default:
		return "", ErrInvalid
	}
	body, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if len(body) > 60<<10 {
		return "", ErrQueueFull
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_tool_operations SET state='succeeded',outcome=? WHERE owner_id=? AND turn_id=? AND id=? AND processing_epoch=? AND state='started'`, string(body), f.OwnerID, f.TurnID, operation, f.ProcessingEpoch); err != nil {
		return "", err
	}
	return string(body), tx.Commit()
}

// DueReminders returns opaque IDs only. Delivery rechecks all authority inside
// the account writer reservation and commits its message and state atomically.
func (r *Repository) DueReminders(ctx context.Context) ([]int64, error) {
	rows, err := r.authorityReads.QueryContext(ctx, `SELECT id FROM mobile_reminders WHERE state='pending' AND due_at<=? AND retry_at<=? ORDER BY due_at,id LIMIT 100`, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // Read-only cleanup.
	var ids []int64
	for rows.Next() {
		var id int64
		if checkErr := rows.Scan(&id); checkErr != nil {
			return nil, checkErr
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeliverReminder atomically commits a scheduled native message and delivery state.
func (r *Repository) DeliverReminder(ctx context.Context, id int64, verify func(context.Context, ApprovalAuthority) error) (delivered bool, deliveryErr error) {
	tx, err := r.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		// Release the writer reservation before scheduling a retry.
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			deliveryErr = errors.Join(deliveryErr, rollbackErr)
			return
		}
		if deliveryErr != nil && ctx.Err() == nil {
			// Recoverable failures must not monopolize the oldest due batch. Never
			// mark delivered or erase private intent on an operational failure.
			_, retryErr := r.db.ExecContext(ctx, `UPDATE mobile_reminders SET retry_at=? WHERE id=? AND state='pending'`, time.Now().Add(time.Minute).UnixMilli(), id)
			deliveryErr = errors.Join(deliveryErr, retryErr)
		}
	}()
	var owner, chat, title, delivery string
	var generation uint64
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT owner_id,conversation_id,generation,title,authority,delivery_id FROM mobile_reminders WHERE id=? AND state='pending' AND due_at<=?`, id, time.Now().UnixMilli()).Scan(&owner, &chat, &generation, &title, &raw, &delivery)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := get(ctx, tx, owner, chat)
	if err != nil {
		return false, err
	}
	if c.Generation != generation || c.Archived {
		_, err = tx.ExecContext(ctx, `UPDATE mobile_reminders SET state='cancelled',title='',authority='{}' WHERE id=?`, id)
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	currentStamp, stampErr := accountStamp(ctx, tx, owner)
	var originStamp string
	var originCancelled bool
	if checkErr := tx.QueryRowContext(ctx, `SELECT t.account_stamp,t.cancellation_requested FROM mobile_turns t JOIN mobile_reminders r ON r.origin_turn=t.id AND r.owner_id=t.owner_id WHERE r.id=?`, id).Scan(&originStamp, &originCancelled); checkErr != nil {
		return false, checkErr
	}
	permanentStamp := errors.Is(stampErr, sql.ErrNoRows) || errors.Is(stampErr, ErrFenced)
	if stampErr != nil && !permanentStamp {
		return false, stampErr
	}
	if permanentStamp || currentStamp != originStamp || originCancelled {
		_, err = tx.ExecContext(ctx, `UPDATE mobile_reminders SET state='cancelled',title='',authority='{}' WHERE id=?`, id)
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	var a ApprovalAuthority
	if json.Unmarshal(raw, &a) != nil || a.OwnerID != owner || a.PrincipalType == "service" || verify == nil {
		return false, ErrApproval
	}
	if err = verify(ctx, a); err != nil {
		if errors.Is(err, ErrReminderRevoked) {
			_, err = tx.ExecContext(ctx, `UPDATE mobile_reminders SET state='cancelled',title='',authority='{}' WHERE id=?`, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE mobile_reminders SET retry_at=? WHERE id=?`, time.Now().Add(time.Minute).UnixMilli(), id)
		}
		if err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	var used int64
	if checkErr := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(CAST(text AS BLOB))),0) FROM mobile_messages WHERE owner_id=?`, owner).Scan(&used); checkErr != nil {
		return false, checkErr
	}
	if used+int64(len(title))+64 > 64<<20 {
		return false, ErrQueueFull
	}
	var seq, turnSeq, admission uint64
	if checkErr := tx.QueryRowContext(ctx, `UPDATE mobile_conversations SET next_turn_seq=next_turn_seq+1,message_seq=message_seq+1,updated_at=? WHERE owner_id=? AND id=? RETURNING next_turn_seq,message_seq`, time.Now().UnixMilli(), owner, chat).Scan(&turnSeq, &seq); checkErr != nil {
		return false, checkErr
	}
	if checkErr := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(admission_seq),0)+1 FROM mobile_turns WHERE owner_id=?`, owner).Scan(&admission); checkErr != nil {
		return false, checkErr
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_transcript_turns(id,owner_id,conversation_id,generation,turn_seq) VALUES(?,?,?,?,?)`, delivery, owner, chat, generation, turnSeq); err != nil {
		return false, err
	}
	digest := sha256.Sum256([]byte(title))
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_turns(id,owner_id,conversation_id,generation,turn_seq,client_message_id,digest,input_message_id,state,profile_id,memory_mode,conversation_version,admission_seq,created_at) VALUES(?,?,?,?,?,?,?,'','completed',?,?,?,?,?)`, delivery, owner, chat, generation, turnSeq, delivery, digest[:], c.Settings.ProfileID, c.Settings.MemoryMode, c.Version, admission, time.Now().UnixMilli()); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_messages(id,owner_id,conversation_id,generation,turn_id,turn_seq,message_seq,role,text,created_at) VALUES(?,?,?,?,?,?,?,'assistant',?,?)`, uuid.NewString(), owner, chat, generation, delivery, turnSeq, seq, fmt.Sprintf("Напоминание: %s", title), time.Now().UnixMilli()); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_reminders SET state='delivered',title='',authority='{}' WHERE id=? AND state='pending'`, id); err != nil {
		return false, err
	}
	if checkErr := r.event(ctx, tx, owner, chat, generation, delivery, turnSeq, "", "turn.changed"); checkErr != nil {
		return false, checkErr
	}
	if checkErr := bump(ctx, tx, owner); checkErr != nil {
		return false, checkErr
	}
	return true, tx.Commit()
}
