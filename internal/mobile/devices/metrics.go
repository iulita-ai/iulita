package devices

import "context"

// Diagnostics contains only bounded, aggregate metadata. Retained gauges cover
// seven days; delivery evidence and successful completion are measured apart.
// Diagnostics contains retained task counts and successful completion timings.
type Diagnostics struct {
	Commands                        [8]int64
	Delivered, Succeeded            int64
	DeliverySeconds, SuccessSeconds float64
}

var commandStates = [8]string{"queued", "dispatched", "started", "succeeded", "failed", "cancelled", "expired", "unknown_outcome"}

// Diagnostics returns bounded subsystem counters.
func (s *Store) Diagnostics(ctx context.Context) (Diagnostics, error) {
	var out Diagnostics
	rows, err := s.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM mobile_phone_commands GROUP BY state`)
	if err != nil {
		return out, err
	}
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var state string
		var count int64
		if err = rows.Scan(&state, &count); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return out, err
		}
		for i, known := range commandStates {
			if state == known {
				out.Commands[i] = count
			}
		}
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(MAX(0,r.at-t.created_at))/1000.0,0) FROM mobile_phone_commands c JOIN mobile_phone_tasks t ON t.id=c.task_id JOIN (SELECT command_id,MIN(created_at) at FROM mobile_phone_receipts GROUP BY command_id) r ON r.command_id=c.id`).Scan(&out.Delivered, &out.DeliverySeconds)
	if err != nil {
		return out, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(MAX(0,finished_at-created_at))/1000.0,0) FROM mobile_phone_tasks WHERE state='succeeded' AND finished_at>0`).Scan(&out.Succeeded, &out.SuccessSeconds)
	return out, err
}
