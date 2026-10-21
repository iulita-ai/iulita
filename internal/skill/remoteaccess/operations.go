package remoteaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	engine "github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

func (s *Skill) executeOperation(ctx context.Context, sc engine.Scope, input json.RawMessage) (string, error) {
	var r engine.ExecuteRequest
	if s.action == "files" {
		var in struct {
			RouteID        string `json:"route_id"`
			Operation      string `json:"operation"`
			Root           string `json:"root"`
			Path           string `json:"path"`
			ArtifactID     string `json:"artifact_id,omitempty"`
			SHA256         string `json:"sha256,omitempty"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		if in.Operation != "file_get" && in.Operation != "file_stat" && in.Operation != "file_put" {
			return "", engine.ErrDenied
		}
		f := &engine.FileRequest{Root: in.Root, Path: in.Path, SHA256: in.SHA256}
		if in.Operation == "file_put" {
			if in.ArtifactID == "" {
				return "", engine.ErrDenied
			}
			a, err := s.service.Artifact(sc, in.ArtifactID)
			if err != nil || a.File == nil {
				return "", engine.ErrDenied
			}
			digest := sha256.Sum256(a.File.Data)
			if a.File.Size != int64(len(a.File.Data)) || a.File.SHA256 != hex.EncodeToString(digest[:]) {
				return "", engine.ErrDenied
			}
			f.Data = append([]byte{}, a.File.Data...)
			f.SHA256 = a.File.SHA256
			if in.SHA256 != "" && in.SHA256 != f.SHA256 {
				return "", engine.ErrDenied
			}
		} else if in.ArtifactID != "" {
			return "", engine.ErrDenied
		}
		r = engine.ExecuteRequest{RouteID: in.RouteID, Operation: in.Operation, File: f, TimeoutSeconds: in.TimeoutSeconds}
	} else {
		var in struct {
			RouteID        string `json:"route_id"`
			Operation      string `json:"operation"`
			Root           string `json:"root"`
			JobID          string `json:"job_id"`
			Command        string `json:"command,omitempty"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		if in.Operation != "background_install" && in.Operation != "background_start" && in.Operation != "background_status" && in.Operation != "background_cancel" {
			return "", engine.ErrDenied
		}
		if in.Operation != "background_start" && in.Command != "" {
			return "", engine.ErrDenied
		}
		r = engine.ExecuteRequest{RouteID: in.RouteID, Operation: in.Operation, Background: &engine.BackgroundRequest{Root: in.Root, JobID: in.JobID, Command: in.Command}, TimeoutSeconds: in.TimeoutSeconds}
	}
	x, err := s.service.ExecuteOperation(ctx, sc, r, func(c context.Context, a engine.Approval) (bool, error) {
		confirmer := interact.ConfirmerFrom(c)
		if confirmer == nil {
			return false, engine.ErrDenied
		}
		var preview strings.Builder
		preview.WriteString("SSH operation\n")
		for i := range a.Servers {
			v := &a.Servers[i]
			fmt.Fprintf(&preview, "%d. %s@%s:%d\n", i+1, a.Identities[i].Username, v.Address, v.Port)
		}
		preview.WriteString("\n" + a.Command + "\n\nOnly this exact operation. Output and file contents remain private. Cancellation does not prove remote work stopped.")
		now := time.Now()
		decision, e := confirmer.Confirm(c, interact.Confirmation{Nonce: a.ExecutionID, OperationID: a.ExecutionID, Fingerprint: a.PlanHash, Reason: interact.ConfirmSSHCommand, Preview: preview.String(), IssuedAt: now, ExpiresAt: a.ExpiresAt}, interact.Actor{UserID: sc.OwnerID})
		return e == nil && decision.Approved && decision.Standing == "" && decision.Actor.UserID == sc.OwnerID, e
	})
	if err != nil {
		return "", engine.ErrDenied
	}
	raw, e := json.Marshal(x)
	return string(raw), e
}
