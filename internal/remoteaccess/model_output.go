package remoteaccess

import (
	"context"
	"encoding/json"
	"time"
)

func ordinaryModelExec(r ExecuteRequest) bool {
	return r.Operation == "" && (r.Capability == "" || r.Capability == "exec") && r.RunbookID == "" && r.RunbookRevision == 0 && r.File == nil && r.Background == nil && r.Terminal == nil
}

// ModelOutputArtifact releases output only from an explicitly approved ordinary
// execution while its exact original authorization remains valid. Artifact IDs
// supplied by callers are never accepted by this API. Exact credential values
// are returned transiently for the trusted adapter to redact: neither these
// values nor the unsanitized artifact may be sent directly to the model.
func (s *Service) ModelOutputArtifact(ctx context.Context, sc Scope, executionID string) (Artifact, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.data.Executions[executionID]
	if ctx.Err() != nil || !ok || !validScope(sc) || s.closed || s.broken || !s.data.Enabled || !x.ModelOutput || x.OwnerID != sc.OwnerID || x.CallerID != sc.CallerID || x.Operation != "" || x.ApprovedAt.IsZero() || x.FinishedAt.IsZero() || (x.State != "completed" && x.State != "failed") || x.GrantID == "" || x.ArtifactID == "" || x.ErrorCode == "artifact_storage_failed" || s.vault == nil || !s.vault.EncryptionEnabled() {
		return Artifact{}, nil, ErrDenied
	}
	stored, exists := s.data.Artifacts[x.ArtifactID]
	if !exists || stored.ModelRequestCiphertext == "" || stored.OwnerID != sc.OwnerID || !stored.ExpiresAt.After(time.Now()) {
		return Artifact{}, nil, ErrDenied
	}
	raw, err := s.vault.OpenPrivate(stored.ModelRequestCiphertext)
	if err != nil {
		return Artifact{}, nil, ErrDenied
	}
	var req ExecuteRequest
	if json.Unmarshal([]byte(raw), &req) != nil || !req.ModelOutput || !ordinaryModelExec(req) || req.RouteID != x.RouteID {
		return Artifact{}, nil, ErrDenied
	}
	plan, err := s.plan(sc, req)
	if err != nil || plan.PlanHash != x.PlanHash || plan.GrantID != x.GrantID || plan.Route.Revision != x.RouteRevision {
		return Artifact{}, nil, ErrDenied
	}
	output, err := s.vault.OpenPrivate(stored.Ciphertext)
	if err != nil {
		return Artifact{}, nil, ErrDenied
	}
	var artifact Artifact
	if json.Unmarshal([]byte(output), &artifact) != nil || artifact.File != nil || artifact.Background != nil || artifact.Command != req.Command {
		return Artifact{}, nil, ErrDenied
	}
	var secrets []string
	for index := range plan.Identities {
		identity := &plan.Identities[index]
		value, err := s.vault.ResolvePrivateAtVersion(ctx, sc.OwnerID, identity.CredentialRef, "ssh_identity", plan.CredentialVersions[index])
		if err != nil {
			return Artifact{}, nil, ErrDenied
		}
		var secret Secret
		if json.Unmarshal([]byte(value), &secret) != nil {
			return Artifact{}, nil, ErrDenied
		}
		for _, value := range []string{secret.Password, secret.PrivateKey, secret.Passphrase} {
			if value != "" {
				secrets = append(secrets, value)
			}
		}
	}
	if ctx.Err() != nil {
		return Artifact{}, nil, ErrDenied
	}
	return artifact, secrets, nil
}
