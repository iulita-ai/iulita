package remoteaccess

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func enableModelGrant(t *testing.T, s *Service, sc Scope) {
	t.Helper()
	//nolint:gocritic // Mutate detached grant snapshots without aliasing catalog state.
	for _, g := range s.Snapshot(sc).Grants {
		g.AllowModelOutput = true
		if _, err := s.PutGrant(sc, g); err != nil {
			t.Fatal(err)
		}
	}
}
func modelExecution(t *testing.T) (*Service, Scope, Execution) {
	t.Helper()
	s, sc, req, _ := setupService(t)
	enableModelGrant(t, s, sc)
	req.ModelOutput = true
	x, err := s.Execute(context.Background(), sc, req, func(_ context.Context, a Approval) (bool, error) {
		if !a.ModelOutput {
			t.Error("missing output consent")
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, sc, x
}
func TestModelOutputExplicitConsentAndPrivatePersistence(t *testing.T) {
	s, sc, req, tr := setupService(t)
	req.ModelOutput = true
	if _, err := s.Preview(sc, req); err == nil {
		t.Fatal("default grant releases model output")
	}
	enableModelGrant(t, s, sc)
	preview, err := s.Preview(sc, req)
	if err != nil {
		t.Fatal(err)
	}
	req.ModelOutput = false
	req.ExpectedPlanHash = preview.PlanHash
	if _, err = s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil }); err == nil || tr.calls != 0 {
		t.Fatal("consent flag changed without rejection")
	}
	s, sc, x := modelExecution(t)
	a, secrets, err := s.ModelOutputArtifact(context.Background(), sc, x.ID)
	if err != nil || a.Stdout != "SECRET-CANARY" || len(secrets) != 1 || secrets[0] != "private-password" {
		t.Fatalf("authorized projection: %v", err)
	}
	b, _ := json.Marshal(x)
	disk, _ := os.ReadFile(s.path)
	for _, data := range [][]byte{b, disk} {
		if strings.Contains(string(data), "printf hello") || strings.Contains(string(data), "SECRET-CANARY") || strings.Contains(string(data), "private-password") {
			t.Fatal("raw model source persisted")
		}
	}
	if _, _, err = s.ModelOutputArtifact(context.Background(), sc, x.ArtifactID); err == nil {
		t.Fatal("arbitrary artifact identifier accepted")
	}
}
func TestModelOutputRechecksExactAuthority(t *testing.T) {
	for _, mode := range []string{"owner", "caller", "legacy", "unapproved", "running", "disabled", "closed", "broken", "grant", "route", "identity", "server", "credential", "policy", "expired_artifact", "principal", "context"} {
		t.Run(mode, func(t *testing.T) {
			s, sc, x := modelExecution(t)
			ctx := context.Background()
			s.mu.Lock()
			switch mode {
			case "owner":
				sc.OwnerID = "other"
			case "caller":
				sc.CallerID = "other"
			case "legacy":
				x.ModelOutput = false
				s.data.Executions[x.ID] = x
			case "unapproved":
				x.ApprovedAt = time.Time{}
				s.data.Executions[x.ID] = x
			case "running":
				x.FinishedAt = time.Time{}
				s.data.Executions[x.ID] = x
			case "disabled":
				s.data.Enabled = false
			case "closed":
				s.closed = true
			case "broken":
				s.broken = true
			case "grant":
				g := s.data.Grants[x.GrantID]
				g.AllowModelOutput = false
				s.data.Grants[g.ID] = g
			case "route":
				r := s.data.Routes[x.RouteID]
				r.Revision++
				s.data.Routes[r.ID] = r
			case "identity":
				for id, i := range s.data.Identities {
					i.ExpiresAt = time.Now().Add(-time.Second)
					s.data.Identities[id] = i
				}
			case "server":
				for id, v := range s.data.Servers {
					v.Enabled = false
					s.data.Servers[id] = v
				}
			case "credential":
				for id, i := range s.data.Identities {
					i.CredentialRef = "missing"
					s.data.Identities[id] = i
				}
			case "policy":
				s.data.PolicyRevision++
			case "expired_artifact":
				a := s.data.Artifacts[x.ArtifactID]
				a.ExpiresAt = time.Now().Add(-time.Second)
				s.data.Artifacts[x.ArtifactID] = a
			case "principal":
				s.principalChecker = func(context.Context, string) bool { return false }
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			s.mu.Unlock()
			if _, _, err := s.ModelOutputArtifact(ctx, sc, x.ID); err == nil {
				t.Fatal("released revoked output")
			}
		})
	}
}
func TestModelOutputOrdinaryExecOnly(t *testing.T) {
	for _, mode := range []string{"terminal", "file", "background", "runbook", "operation", "capability"} {
		t.Run(mode, func(t *testing.T) {
			s, sc, req, _ := setupService(t)
			enableModelGrant(t, s, sc)
			req.ModelOutput = true
			switch mode {
			case "terminal":
				req.Terminal = &TerminalOptions{}
			case "file":
				req.File = &FileRequest{}
			case "background":
				req.Background = &BackgroundRequest{}
			case "runbook":
				req.RunbookID = "book"
			case "operation":
				req.Operation = "terminal"
			case "capability":
				req.Capability = "runbook"
			}
			if _, err := s.Preview(sc, req); err == nil {
				t.Fatal("noninteractive output accepted")
			}
		})
	}
}

// Request ciphertext is part of the same persisted artifact budget as output.
// Exercise both an older request and the newly completed request; omitting
// either one from accounting leaves the synthetic old artifact unevicted.
func TestModelOutputRetentionCountsEncryptedRequests(t *testing.T) {
	for _, olderRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "existing"}[olderRequest], func(t *testing.T) {
			s, sc, req, _ := setupService(t)
			enableModelGrant(t, s, sc)
			req.ModelOutput = true
			old := sealedArtifact{OwnerID: sc.OwnerID, ExpiresAt: time.Now().Add(time.Hour)}
			if olderRequest {
				old.Ciphertext = strings.Repeat("a", (64<<20)-(16<<10))
				old.ModelRequestCiphertext = strings.Repeat("b", 32<<10)
			} else {
				old.Ciphertext = strings.Repeat("a", (64<<20)-(64<<10))
				req.Command = strings.Repeat("x", 32<<10)
			}
			s.mu.Lock()
			s.data.Artifacts["older"] = old
			s.mu.Unlock()
			x, err := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil })
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			_, retained := s.data.Artifacts["older"]
			s.mu.Unlock()
			if retained {
				t.Fatal("request ciphertext was excluded from retention budget")
			}
			if _, _, err := s.ModelOutputArtifact(context.Background(), sc, x.ID); err != nil {
				t.Fatalf("current artifact was not retained: %v", err)
			}
		})
	}
}

func TestModelOutputCancelledIntentNeverReleases(t *testing.T) {
	s, sc, req, _ := setupService(t)
	enableModelGrant(t, s, sc)
	req.ModelOutput = true
	x, err := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return false, nil })
	if err == nil || x.State != "cancelled" {
		t.Fatal("expected cancelled intent")
	}
	s.mu.Lock()
	intent := s.data.Artifacts[x.ArtifactID]
	s.mu.Unlock()
	if intent.ModelRequestCiphertext == "" {
		t.Fatal("encrypted intent was lost")
	}
	if _, _, err := s.ModelOutputArtifact(context.Background(), sc, x.ID); err == nil {
		t.Fatal("cancelled intent released")
	}
}
