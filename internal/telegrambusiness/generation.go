package telegrambusiness

import "context"

type generationKey struct{}

// GenerationSettings binds provider consent and finite model limits to this
// immutable turn. The adapter must honor these settings without fallback.
type GenerationSettings struct {
	Provider          string
	MaxInputTokens    int
	MaxOutputTokens   int
	Operation         string
	Epoch             uint64
	ContextRevision   uint64
	RuntimeGeneration string
	RestoreGeneration string
}

// GenerationSettingsFrom returns the service-issued immutable generation policy.
func GenerationSettingsFrom(ctx context.Context) (GenerationSettings, bool) {
	v, ok := ctx.Value(generationKey{}).(GenerationSettings)
	return v, ok
}

// ReconcileGeneration adopts a delivery generation only after fresh metadata.
func (s *Service) ReconcileGeneration(ctx context.Context, key Key, generation string) error {
	if generation == "" || len(generation) > 128 {
		return ErrDenied
	}
	var before Connection
	if checkErr := s.update(ctx, key, func(st *State) error { before = st.Connection; return nil }); checkErr != nil {
		return checkErr
	}
	if before.OwnerID == "" {
		return ErrUnavailable
	}
	if before.DeliveryGeneration == generation && before.RuntimeGeneration == s.options.RuntimeGeneration && !before.NeedsRefresh {
		return nil
	}
	if checkErr := s.Reconcile(ctx, key); checkErr != nil {
		return checkErr
	}
	var current Connection
	if checkErr := s.update(ctx, key, func(st *State) error { current = st.Connection; return nil }); checkErr != nil {
		return checkErr
	}
	e := s.update(ctx, key, func(st *State) error {
		if st.Connection.Epoch != current.Epoch || st.Connection.OwnerID != current.OwnerID {
			return ErrStale
		}
		st.Connection.DeliveryGeneration = generation
		st.Connection.ControlUpdateID = -1
		invalidate(st)
		return nil
	})
	if e == nil {
		s.cancelKey(key)
	}
	return e
}
