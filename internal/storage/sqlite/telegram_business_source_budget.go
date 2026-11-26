package sqlite

import (
	"context"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/uptrace/bun"
)

// ReserveSource atomically debits UTC-day STT calls and worst-case seconds for
// peer, connection, owner and instance. No result or cancellation refunds them.
func (r *TelegramBusinessRepository) ReserveSource(ctx context.Context, request business.SourceBudgetRequest) error {
	if !validBusinessKey(request.Scope.Key) || request.Scope.OwnerID == "" || request.Scope.PeerID <= 0 || request.Bytes <= 0 || request.Bytes > business.MaxMediaBytes || request.DurationSeconds <= 0 || request.DurationSeconds > 120 || request.At.IsZero() {
		return business.ErrDenied
	}
	return r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		row, err := readBusinessRow(ctx, db, request.Scope.Key)
		if err != nil {
			return err
		}
		if row == nil || row.OwnerID != request.Scope.OwnerID {
			return business.ErrDenied
		}
		state, err := r.decode(ctx, db, row, false)
		if err != nil {
			return err
		}
		connection := state.Connection
		peer := state.Peers[request.Scope.PeerID]
		if !connection.Enabled || connection.Paused || connection.Quarantined || connection.NeedsRefresh || !connection.Policy.Enabled || !connection.Policy.Consent || !connection.Policy.MediaConsent || !connection.Policy.TokenOnly || connection.Policy.MediaProvider == "" || peer == nil || !peer.Policy.Enabled || !peer.Policy.Consent || !peer.Policy.MediaConsent || !peer.Policy.TokenOnly || peer.Policy.MediaProvider != connection.Policy.MediaProvider {
			return business.ErrDenied
		}
		at := request.At
		if now := time.Now().UTC(); now.After(at) {
			at = now
		}
		day := at.UTC().Format("2006-01-02")
		limits := []struct{ calls, seconds int64 }{{10, 600}, {30, 1800}, {60, 3600}, {240, 14400}}
		scopes := businessBudgetScopes(request.Scope)
		for i, scope := range scopes {
			if scope.Key == "" {
				return business.ErrUnavailable
			}
			var usage struct {
				Calls   int64 `bun:"calls"`
				Seconds int64 `bun:"duration_seconds"`
				Bytes   int64 `bun:"bytes"`
			}
			if usageErr := db.NewRaw(`SELECT COALESCE(SUM(calls),0) AS calls,COALESCE(SUM(duration_seconds),0) AS duration_seconds,COALESCE(SUM(bytes),0) AS bytes FROM telegram_business_source_usage WHERE scope_key=? AND day=?`, scope.Key, day).Scan(ctx, &usage); usageErr != nil {
				return usageErr
			}
			limit := limits[i]
			if usage.Calls < 0 || usage.Seconds < 0 || usage.Bytes < 0 || usage.Calls >= limit.calls || request.DurationSeconds > limit.seconds-usage.Seconds || request.Bytes > limit.calls*business.MaxMediaBytes-usage.Bytes {
				return business.ErrQuota
			}
			if _, chargeErr := db.ExecContext(ctx, `INSERT INTO telegram_business_source_usage(scope_key,day,calls,duration_seconds,bytes) VALUES(?,?,1,?,?) ON CONFLICT(scope_key,day) DO UPDATE SET calls=calls+1,duration_seconds=duration_seconds+excluded.duration_seconds,bytes=bytes+excluded.bytes`, scope.Key, day, request.DurationSeconds, request.Bytes); chargeErr != nil {
				return chargeErr
			}
		}
		return nil
	})
}

var _ business.SourceBudget = (*TelegramBusinessRepository)(nil)
