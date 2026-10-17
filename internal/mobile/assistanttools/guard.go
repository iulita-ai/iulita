package assistanttools

import (
	"context"
	"time"

	"github.com/iulita-ai/iulita/internal/skill"
)

// Cancellation follows service disconnect/reassignment/rotation as well as the
// turn's OAuth guard. An already dispatched mutation is never declared unsent.
func watchBinding(ctx context.Context, r *skill.Registry, s skill.Skill, p skill.NativePolicy, owner string) (context.Context, context.CancelFunc) {
	guarded, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-guarded.Done():
				return
			case <-ticker.C:
				current, ok := r.Get(s.Name())
				if !ok || current != s {
					cancel()
					return
				}
				if d, ok := s.(skill.NativeDeclarer); !ok || d.NativePolicy().Revision != p.Revision {
					cancel()
					return
				}
				if a, ok := s.(availability); ok && !a.NativeAvailable(guarded, owner) {
					cancel()
					return
				}
			}
		}
	}()
	return guarded, cancel
}
