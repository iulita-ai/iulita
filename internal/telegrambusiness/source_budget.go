package telegrambusiness

import (
	"context"
	"time"
)

// SourceBudgetRequest reserves one worst-case transcription attempt. Raw audio,
// endpoints and correspondence never belong in this persistent numeric ledger.
type SourceBudgetRequest struct {
	Scope           Scope
	Bytes           int64
	DurationSeconds int64
	At              time.Time
}

// SourceBudget debits all scopes atomically before any billable STT request.
// Failed, cancelled and unknown attempts are never refunded.
type SourceBudget interface {
	ReserveSource(context.Context, SourceBudgetRequest) error
}

type sourceBudgetKey struct{}

// WithSourceBudget attaches a server-owned, persistent transcription budget.
func WithSourceBudget(ctx context.Context, budget SourceBudget) context.Context {
	return context.WithValue(ctx, sourceBudgetKey{}, budget)
}

// ReserveSource requires current source authority and a finite numeric debit.
// Missing budget dependencies fail closed before any provider request.
func ReserveSource(ctx context.Context, scope Scope, bytes, duration int64) error {
	if scope.validate() != nil || scope.PeerID <= 0 || bytes <= 0 || bytes > MaxMediaBytes || duration <= 0 || duration > 120 {
		return ErrDenied
	}
	if err := CheckSourceGuard(ctx); err != nil {
		return err
	}
	budget, ok := ctx.Value(sourceBudgetKey{}).(SourceBudget)
	if !ok || budget == nil {
		return ErrUnavailable
	}
	return budget.ReserveSource(ctx, SourceBudgetRequest{Scope: scope, Bytes: bytes, DurationSeconds: duration, At: time.Now().UTC()})
}
