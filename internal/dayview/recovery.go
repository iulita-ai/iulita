package dayview

import (
	"context"
	"encoding/json"
	"time"
)

// Verification records a read-only observation without proving which request caused it.
type Verification string

// Verification outcomes describe observation and never authorize replay.
const (
	// EffectObserved means the desired state is visible; it does not prove causation.
	EffectObserved Verification = "effect_observed"
	NotObserved    Verification = "not_observed"
	Inconclusive   Verification = "inconclusive"
	Revoked        Verification = "revoked"
)

// VerifySource observes the exact pinned resource. It must never write or
// replace an unavailable connection with another/default account.
type VerifySource interface {
	Verify(context.Context, string, Item, string, time.Time) (Verification, error)
}

// Command pins the exact item and approved change for read-only reconciliation.
type Command struct {
	Item   Item      `json:"item"`
	Action string    `json:"action"`
	Due    time.Time `json:"due"`
}

// RecoveryStore persists scoped receipts and conditional verification outcomes.
type RecoveryStore interface {
	ListDayOperations(context.Context, string, string) ([]Operation, error)
	ApplyDayVerification(context.Context, string, string, string, Verification) error
}

// RecoverOperations performs bounded read-only recovery of uncertain effects.
// Observing the desired state is not evidence that this operation caused it.
func RecoverOperations(ctx context.Context, store RecoveryStore, service *Service, owner, conversation string) ([]Operation, error) {
	operations, e := store.ListDayOperations(ctx, owner, conversation)
	if e != nil {
		return nil, e
	}
	if service == nil {
		return operations, nil
	}
	checked := 0
	for index := range operations {
		op := &operations[index]
		if checked >= 3 || ctx.Err() != nil {
			break
		}
		if op.Status != "unknown_outcome" && op.Status != "running" && op.Status != "inconclusive" && op.Status != "not_observed" && op.Status != "revoked" {
			continue
		}
		if time.Since(op.CreatedAt) < 3*time.Minute && op.Status == "running" {
			continue
		}
		if !op.LastCheckedAt.IsZero() && time.Since(op.LastCheckedAt) < time.Minute {
			continue
		}
		var command Command
		if json.Unmarshal([]byte(op.CommandJSON), &command) != nil || command.Item.ID == "" {
			continue
		}
		checked++
		outcome := Inconclusive
		for _, source := range service.Sources {
			if source.Name() == command.Item.Source {
				if verifier, ok := source.(VerifySource); ok {
					call, cancel := context.WithTimeout(ctx, time.Second)
					v, err := verifier.Verify(call, owner, command.Item, command.Action, command.Due)
					cancel()
					if err == nil {
						outcome = v
					}
					break
				}
			}
		}
		if applyErr := store.ApplyDayVerification(ctx, owner, op.ID, op.Status, outcome); applyErr != nil {
			return nil, applyErr
		}
	}
	return store.ListDayOperations(ctx, owner, conversation)
}
