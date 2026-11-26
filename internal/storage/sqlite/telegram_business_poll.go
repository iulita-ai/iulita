package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/uptrace/bun"
)

func validBusinessPoll(instance string, bot int64) bool {
	return instance != "" && len(instance) <= 256 && bot > 0
}

// AcquireTelegramBusinessPoll reserves one database-backed bot consumer lease.
func (r *TelegramBusinessRepository) AcquireTelegramBusinessPoll(ctx context.Context, instance string, bot int64, owner string, at time.Time, ttl time.Duration) (bool, error) {
	if !validBusinessPoll(instance, bot) || owner == "" || len(owner) > 256 || ttl <= 0 || ttl > 2*time.Minute {
		return false, business.ErrDenied
	}
	claimed := false
	err := r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		leaseNow := at
		if now := time.Now().UTC(); now.After(leaseNow) {
			leaseNow = now
		}
		// Telegram's cloud cursor belongs to the bot token, independently of
		// local instance names. The database writer lock serializes this check
		// with every competing instance's lease acquisition.
		var competing int
		if err := db.NewRaw(`SELECT COUNT(*) FROM telegram_business_poll WHERE bot_id=? AND instance_id<>? AND lease_until>?`, bot, instance, leaseNow).Scan(ctx, &competing); err != nil {
			return err
		}
		if competing != 0 {
			return nil
		}
		res, err := db.ExecContext(ctx, `INSERT INTO telegram_business_poll(instance_id,bot_id,next_offset,updated_at,lease_owner,lease_until) VALUES(?,?,0,?,?,?) ON CONFLICT(instance_id,bot_id) DO UPDATE SET lease_owner=excluded.lease_owner,lease_until=excluded.lease_until WHERE telegram_business_poll.lease_owner=? OR telegram_business_poll.lease_until<=?`, instance, bot, at, owner, leaseNow.Add(ttl), owner, leaseNow)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		claimed = n == 1
		return err
	})
	return claimed, err
}

// ReleaseTelegramBusinessPoll releases only the exact holder's lease.
func (r *TelegramBusinessRepository) ReleaseTelegramBusinessPoll(ctx context.Context, instance string, bot int64, owner string) error {
	if !validBusinessPoll(instance, bot) || owner == "" {
		return business.ErrDenied
	}
	_, err := r.store.db.ExecContext(ctx, `UPDATE telegram_business_poll SET lease_owner='',lease_until=? WHERE instance_id=? AND bot_id=? AND lease_owner=?`, time.Time{}, instance, bot, owner)
	return err
}

// LoadTelegramBusinessPoll returns the durable cursor without acquiring authority.
func (r *TelegramBusinessRepository) LoadTelegramBusinessPoll(ctx context.Context, instance string, bot int64) (business.PollState, error) {
	if !validBusinessPoll(instance, bot) {
		return business.PollState{}, business.ErrDenied
	}
	var row struct {
		NextOffset int64     `bun:"next_offset"`
		UpdatedAt  time.Time `bun:"updated_at"`
	}
	err := r.store.db.NewRaw(`SELECT next_offset,updated_at FROM telegram_business_poll WHERE instance_id=? AND bot_id=?`, instance, bot).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return business.PollState{}, nil
	}
	return business.PollState{NextOffset: row.NextOffset, UpdatedAt: row.UpdatedAt}, err
}

func pollBinding(instance string, bot, update int64) string {
	b, err := json.Marshal([]any{"business-ordinary-poll-v1", instance, bot, update})
	if err != nil {
		return ""
	}
	return string(b)
}

func ordinaryPollPayload(entry business.PollEntry) bool {
	if entry.UpdateID < 0 || len(entry.Payload) == 0 || len(entry.Payload) > maxBusinessStateBytes {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(entry.Payload, &fields) != nil || fields == nil {
		return false
	}
	var actual int64
	if json.Unmarshal(fields["update_id"], &actual) != nil || actual != entry.UpdateID {
		return false
	}
	for _, kind := range []string{"business_connection", "business_message", "edited_business_message", "deleted_business_messages"} {
		if value, ok := fields[kind]; ok && string(value) != "null" {
			return false
		}
	}
	return true
}

// StageTelegramBusinessPoll atomically queues admitted ordinary updates and the
// next cursor. Business updates must already have committed through the core.
func (r *TelegramBusinessRepository) StageTelegramBusinessPoll(ctx context.Context, instance string, bot int64, owner string, next int64, entries []business.PollEntry, at time.Time) error {
	if !validBusinessPoll(instance, bot) || owner == "" || next < 0 || len(entries) > 100 {
		return business.ErrDenied
	}
	sealed := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if !ordinaryPollPayload(entry) || entry.UpdateID >= next {
			return business.ErrDenied
		}
		value, err := r.seal(pollBinding(instance, bot, entry.UpdateID), entry.Payload)
		if err != nil {
			return err
		}
		sealed[entry.UpdateID] = value
	}
	return r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		var lease struct {
			Owner      string    `bun:"lease_owner"`
			Until      time.Time `bun:"lease_until"`
			NextOffset int64     `bun:"next_offset"`
			UpdatedAt  time.Time `bun:"updated_at"`
		}
		if err := db.NewRaw(`SELECT lease_owner,lease_until,next_offset,updated_at FROM telegram_business_poll WHERE instance_id=? AND bot_id=?`, instance, bot).Scan(ctx, &lease); err != nil {
			return err
		}
		checkAt := at
		if now := time.Now().UTC(); now.After(checkAt) {
			checkAt = now
		}
		if lease.Owner != owner || !lease.Until.After(checkAt) {
			return business.ErrDenied
		}
		// Telegram may reset update IDs after a long idle interval. Only an
		// explicitly observed idle week allows a lower offset in the same lease.
		if next < lease.NextOffset && at.Sub(lease.UpdatedAt) < 7*24*time.Hour {
			return business.ErrStale
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM telegram_business_poll_entries WHERE instance_id=? AND bot_id=? AND created_at<?`, instance, bot, at.Add(-24*time.Hour)); err != nil {
			return err
		}
		for id, value := range sealed {
			if _, err := db.ExecContext(ctx, `INSERT INTO telegram_business_poll_entries(instance_id,bot_id,update_id,payload,created_at) VALUES(?,?,?,?,?) ON CONFLICT(instance_id,bot_id,update_id) DO NOTHING`, instance, bot, id, value, at); err != nil {
				return err
			}
		}
		var count int
		if err := db.NewRaw(`SELECT COUNT(*) FROM telegram_business_poll_entries WHERE instance_id=? AND bot_id=?`, instance, bot).Scan(ctx, &count); err != nil {
			return err
		}
		if count > 1000 {
			return business.ErrQuota
		}
		_, err := db.ExecContext(ctx, `UPDATE telegram_business_poll SET next_offset=?,updated_at=? WHERE instance_id=? AND bot_id=? AND lease_owner=?`, next, at, instance, bot, owner)
		return err
	})
}

// PendingTelegramBusinessPoll opens a bounded unexpired ordinary-update batch.
func (r *TelegramBusinessRepository) PendingTelegramBusinessPoll(ctx context.Context, instance string, bot int64, limit int) ([]business.PollEntry, error) {
	if !validBusinessPoll(instance, bot) || limit <= 0 || limit > 100 {
		return nil, business.ErrDenied
	}
	var rows []struct {
		UpdateID int64  `bun:"update_id"`
		Payload  string `bun:"payload"`
	}
	if err := r.store.db.NewRaw(`SELECT update_id,payload FROM telegram_business_poll_entries WHERE instance_id=? AND bot_id=? AND created_at>=? ORDER BY update_id LIMIT ?`, instance, bot, time.Now().UTC().Add(-24*time.Hour), limit).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	result := make([]business.PollEntry, 0, len(rows))
	for _, row := range rows {
		body, err := r.open(pollBinding(instance, bot, row.UpdateID), row.Payload)
		if err != nil {
			return nil, err
		}
		entry := business.PollEntry{UpdateID: row.UpdateID, Payload: body}
		if !ordinaryPollPayload(entry) {
			return nil, fmt.Errorf("invalid admitted ordinary update: %w", business.ErrUnavailable)
		}
		result = append(result, entry)
	}
	return result, nil
}

// AckTelegramBusinessPoll removes only the exact dispatched update receipt.
func (r *TelegramBusinessRepository) AckTelegramBusinessPoll(ctx context.Context, instance string, bot, update int64) error {
	if !validBusinessPoll(instance, bot) || update < 0 {
		return business.ErrDenied
	}
	_, err := r.store.db.ExecContext(ctx, `DELETE FROM telegram_business_poll_entries WHERE instance_id=? AND bot_id=? AND update_id=?`, instance, bot, update)
	return err
}

var _ business.PollRepository = (*TelegramBusinessRepository)(nil)
