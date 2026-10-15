package engine

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
)

// acceptedBudget caps extensions at one fixed business deadline. Native task,
// session, approval and credential deadlines may only shorten that budget.
func acceptedBudget(root context.Context, absolute time.Time) skill.DeadlineExtender {
	return func(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
		remaining := time.Until(absolute)
		if d > remaining {
			d = remaining
		}
		return skill.LifecycleExtender(root)(ctx, d)
	}
}
