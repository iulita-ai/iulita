package sqlite

import (
	"context"
	"log/slog"
	"time"
)

// RunDayMaintenance is process-scoped, independent of card visibility flags.
// Disabling the UI must not disable expiration of its previously stored data.
func (s *Store) RunDayMaintenance(ctx context.Context) {
	if err := s.CleanupDayState(ctx, time.Now(), 100); err != nil {
		slog.WarnContext(ctx, "Day maintenance failed; retrying on the next maintenance tick")
	}
	timer := time.NewTicker(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := s.CleanupDayState(ctx, time.Now(), 100); err != nil {
				slog.WarnContext(ctx, "Day maintenance failed; retrying on the next maintenance tick")
			}
		}
	}
}
