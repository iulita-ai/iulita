package remoteaccess

import (
	"context"
	"fmt"

	engine "github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// deliverPrivateResult intentionally does not return remote bytes to Execute.
// Reading is scoped to the authenticated owner, never an artifact ID supplied by
// the model. A delivery failure does not change execution state or retry SSH.
func (s *Skill) deliverPrivateResult(ctx context.Context, sc engine.Scope, x engine.Execution) string {
	deliverer, ok := interact.ConfirmerFrom(ctx).(interact.PrivateResultDeliverer)
	if !ok || x.ArtifactID == "" || x.ApprovedAt.IsZero() {
		return "available_in_remote_access"
	}
	a, err := s.service.Artifact(sc, x.ArtifactID)
	if err != nil {
		return "available_in_remote_access"
	}
	code := "unavailable"
	if x.ExitCode != nil {
		code = fmt.Sprint(*x.ExitCode)
	}
	text := fmt.Sprintf("SSH execution %s\nState: %s; exit code: %s\n\nstdout:\n%s\n\nstderr:\n%s", x.ID, x.State, code, a.Stdout, a.Stderr)
	if err := deliverer.DeliverPrivateResult(ctx, interact.Actor{UserID: sc.OwnerID}, x.ID, text); err != nil {
		return "available_in_remote_access"
	}
	return "sent_to_requesting_user"
}
