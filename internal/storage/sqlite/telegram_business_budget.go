package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/uptrace/bun"
)

type businessBudgetScope struct {
	Key                  string
	Concurrent, Attempts int64
	Input, Output, Cost  int64
}

func businessBudgetKey(parts ...any) string {
	encoded, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func businessBudgetScopes(scope business.Scope) []businessBudgetScope {
	k := scope.Key
	return []businessBudgetScope{
		{Key: businessBudgetKey("peer", k.InstanceID, k.BotID, k.ConnectionID, scope.OwnerID, scope.PeerID), Concurrent: 1, Attempts: 10},
		{Key: businessBudgetKey("connection", k.InstanceID, k.BotID, k.ConnectionID, scope.OwnerID), Concurrent: 2, Attempts: 50, Input: 250000, Output: 50000, Cost: 1000000},
		{Key: businessBudgetKey("owner", scope.OwnerID), Concurrent: 4, Attempts: 100, Input: 500000, Output: 100000, Cost: 3000000},
		{Key: businessBudgetKey("instance", k.InstanceID, k.BotID), Concurrent: 8, Attempts: 500, Input: 2000000, Output: 400000, Cost: 10000000},
	}
}

// Reserve atomically reserves all four budgets before a billable provider call.
// Daily worst-case usage remains consumed after cancellation or provider failure.
func (r *TelegramBusinessRepository) Reserve(ctx context.Context, request business.BudgetRequest) (business.Reservation, error) {
	if !validBusinessKey(request.Scope.Key) || request.Scope.OwnerID == "" || request.Scope.PeerID <= 0 || request.Provider == "" || len(request.Provider) > 256 || request.At.IsZero() || !request.ExpiresAt.After(request.At) || request.ExpiresAt.Sub(request.At) > 5*time.Minute || request.InputTokens < 0 || request.InputTokens > 16000 || request.OutputTokens <= 0 || request.OutputTokens > 1000 || request.CostMicros < 0 || (request.TokenOnly && request.CostMicros != 0) {
		return business.Reservation{}, business.ErrDenied
	}
	reservation := business.Reservation{ID: uuid.NewString(), ExpiresAt: request.ExpiresAt}
	err := r.store.runWriteTransaction(ctx, func(ctx context.Context, db bun.IDB) error {
		admittedAt := request.At
		if now := time.Now().UTC(); now.After(admittedAt) {
			admittedAt = now
		}
		if !request.ExpiresAt.After(admittedAt) {
			return business.ErrStale
		}
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
		peer := state.Peers[request.Scope.PeerID]
		if !state.Connection.Enabled || state.Connection.Paused || state.Connection.Quarantined || state.Connection.NeedsRefresh || !state.Connection.Policy.Enabled || !state.Connection.Policy.Consent || state.Connection.Policy.Provider != request.Provider || peer == nil || !peer.Policy.Enabled || !peer.Policy.Consent || peer.Policy.Mode == business.ModeOff || peer.Policy.Provider != request.Provider || peer.Policy.TokenOnly != request.TokenOnly {
			return business.ErrDenied
		}
		if request.TokenOnly && !state.Connection.Policy.TokenOnly {
			return business.ErrDenied
		}
		day := admittedAt.UTC().Format("2006-01-02")
		scopes := businessBudgetScopes(request.Scope)
		keys := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			if scope.Key == "" {
				return business.ErrUnavailable
			}
			var active int64
			if countErr := db.NewRaw(`SELECT COUNT(*) FROM telegram_business_budget_reservations r WHERE r.released=FALSE AND r.expires_at>? AND EXISTS(SELECT 1 FROM json_each(r.scope_keys) WHERE value=?)`, admittedAt, scope.Key).Scan(ctx, &active); countErr != nil {
				return countErr
			}
			if active >= scope.Concurrent {
				return business.ErrQuota
			}
			var usage struct {
				Attempts int64 `bun:"attempts"`
				Input    int64 `bun:"input_tokens"`
				Output   int64 `bun:"output_tokens"`
				Cost     int64 `bun:"cost_micros"`
			}
			if usageErr := db.NewRaw(`SELECT COALESCE(SUM(attempts),0) AS attempts,COALESCE(SUM(input_tokens),0) AS input_tokens,COALESCE(SUM(output_tokens),0) AS output_tokens,COALESCE(SUM(cost_micros),0) AS cost_micros FROM telegram_business_budget_usage WHERE scope_key=? AND day=?`, scope.Key, day).Scan(ctx, &usage); usageErr != nil {
				return usageErr
			}
			if usage.Attempts >= scope.Attempts || (scope.Input > 0 && request.InputTokens > scope.Input-usage.Input) || (scope.Output > 0 && request.OutputTokens > scope.Output-usage.Output) || (!request.TokenOnly && scope.Cost > 0 && request.CostMicros > scope.Cost-usage.Cost) {
				return business.ErrQuota
			}
			cost := request.CostMicros
			if request.TokenOnly {
				cost = 0
			}
			if _, chargeErr := db.ExecContext(ctx, `INSERT INTO telegram_business_budget_usage(scope_key,day,attempts,input_tokens,output_tokens,cost_micros) VALUES(?,?,1,?,?,?) ON CONFLICT(scope_key,day) DO UPDATE SET attempts=attempts+1,input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens,cost_micros=cost_micros+excluded.cost_micros`, scope.Key, day, request.InputTokens, request.OutputTokens, cost); chargeErr != nil {
				return chargeErr
			}
			keys = append(keys, scope.Key)
		}
		encoded, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `INSERT INTO telegram_business_budget_reservations(id,scope_keys,expires_at,released) VALUES(?,?,?,FALSE)`, reservation.ID, string(encoded), request.ExpiresAt)
		return err
	})
	if err != nil {
		return business.Reservation{}, err
	}
	return reservation, nil
}

// Release frees only concurrency; its random reservation ID is an internal
// capability. Daily attempts/tokens/cost are deliberately never refunded.
func (r *TelegramBusinessRepository) Release(ctx context.Context, reservation business.Reservation) error {
	if reservation.ID == "" || len(reservation.ID) > 128 {
		return business.ErrDenied
	}
	_, err := r.store.db.ExecContext(ctx, `UPDATE telegram_business_budget_reservations SET released=TRUE WHERE id=? AND expires_at=?`, reservation.ID, reservation.ExpiresAt)
	return err
}

var _ business.Budget = (*TelegramBusinessRepository)(nil)
