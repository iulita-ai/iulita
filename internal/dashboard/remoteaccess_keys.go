package dashboard

import (
	"context"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
)

type remotePending struct {
	owner    string
	approval remoteaccess.Approval
	result   chan bool
}
type remoteKeyStatus struct {
	ID             string    `json:"id"`
	OwnerID        string    `json:"-"`
	Action         string    `json:"action"`
	State          string    `json:"state"`
	ErrorCode      string    `json:"error_code,omitempty"`
	InstallationID string    `json:"installation_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type remoteConfirmations struct {
	mu         sync.Mutex
	pending    map[string]*remotePending
	operations map[string]remoteKeyStatus
}

func newRemoteConfirmations() *remoteConfirmations {
	return &remoteConfirmations{pending: map[string]*remotePending{}, operations: map[string]remoteKeyStatus{}}
}
func (b *remoteConfirmations) confirm(owner string) remoteaccess.ConfirmFunc {
	return func(ctx context.Context, a remoteaccess.Approval) (bool, error) {
		pending := &remotePending{owner: owner, approval: a, result: make(chan bool, 1)}
		b.mu.Lock()
		b.pending[a.ExecutionID] = pending
		b.mu.Unlock()
		defer func() { b.mu.Lock(); delete(b.pending, a.ExecutionID); b.mu.Unlock() }()
		timer := time.NewTimer(time.Until(a.ExpiresAt))
		defer timer.Stop()
		select {
		case approved := <-pending.result:
			return approved, nil
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
			return false, context.DeadlineExceeded
		}
	}
}
func (s *Server) remoteApprovals(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	out := []remoteaccess.Approval{}
	s.remoteConfirms.mu.Lock()
	defer s.remoteConfirms.mu.Unlock()
	for _, p := range s.remoteConfirms.pending {
		if p.owner == sc.OwnerID && p.approval.ExpiresAt.After(time.Now()) {
			out = append(out, p.approval)
		}
	}
	return c.JSON(out)
}
func (s *Server) remoteApprove(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var body struct {
		PlanHash string `json:"plan_hash"`
		Approved bool   `json:"approved"`
	}
	if bodyErr := remoteBody(c, &body); bodyErr != nil {
		return bodyErr
	}
	s.remoteConfirms.mu.Lock()
	defer s.remoteConfirms.mu.Unlock()
	p, ok := s.remoteConfirms.pending[c.Params("id")]
	if !ok || p.owner != sc.OwnerID || p.approval.PlanHash != body.PlanHash || !p.approval.ExpiresAt.After(time.Now()) {
		return fiber.ErrNotFound
	}
	delete(s.remoteConfirms.pending, c.Params("id"))
	p.result <- body.Approved
	return c.JSON(fiber.Map{"accepted": true})
}
func (s *Server) remoteGenerateIdentity(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	if auth.GetClaims(c).Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	var x remoteaccess.Identity
	if bodyErr := remoteBody(c, &x); bodyErr != nil {
		return bodyErr
	}
	out, err := s.remoteAccess.GenerateIdentity(c.UserContext(), sc, x)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(out)
}
func (s *Server) remoteKeyOperation(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	if auth.GetClaims(c).Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	var x struct {
		Action         string `json:"action"`
		RouteID        string `json:"route_id"`
		IdentityID     string `json:"identity_id"`
		InstallationID string `json:"installation_id"`
		AlternateID    string `json:"alternate_id"`
		NewRouteID     string `json:"new_route_id"`
	}
	if bodyErr := remoteBody(c, &x); bodyErr != nil {
		return bodyErr
	}
	if x.Action != "install" && x.Action != "verify" && x.Action != "remove" && x.Action != "rotate" && x.Action != "reconcile" {
		return fiber.ErrBadRequest
	}
	operationID := uuid.NewString()
	s.remoteConfirms.mu.Lock()
	active := 0
	for id, op := range s.remoteConfirms.operations {
		if op.State != "running" && time.Since(op.CreatedAt) > 24*time.Hour {
			delete(s.remoteConfirms.operations, id)
			continue
		}
		if op.OwnerID == sc.OwnerID && op.State == "running" {
			active++
		}
	}
	if active >= 2 || len(s.remoteConfirms.operations) >= 200 {
		s.remoteConfirms.mu.Unlock()
		return fiber.NewError(429, "Key operation limit reached")
	}
	s.remoteConfirms.operations[operationID] = remoteKeyStatus{ID: operationID, OwnerID: sc.OwnerID, Action: x.Action, State: "running", CreatedAt: time.Now()}
	s.remoteConfirms.mu.Unlock()
	// Every generated script is still authorized and durably logged by Service.
	// The broker can only supply consent from the same authenticated owner.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		confirmer := s.remoteConfirms.confirm(sc.OwnerID)
		var result remoteaccess.KeyInstallation
		var runErr error
		switch x.Action {
		case "install":
			result, runErr = s.remoteAccess.InstallPublicKey(ctx, sc, x.RouteID, x.IdentityID, confirmer)
		case "verify":
			result, runErr = s.remoteAccess.VerifyInstallation(ctx, sc, x.InstallationID, x.RouteID, confirmer)
		case "remove":
			result, runErr = s.remoteAccess.RemovePublicKey(ctx, sc, x.InstallationID, x.AlternateID, x.RouteID, confirmer)
		case "reconcile":
			result, runErr = s.remoteAccess.ReconcilePublicKey(ctx, sc, x.InstallationID, x.RouteID, confirmer)
		case "rotate":
			result, runErr = s.remoteAccess.RotatePublicKey(ctx, sc, x.InstallationID, x.RouteID, x.IdentityID, x.NewRouteID, confirmer)
		}
		s.remoteConfirms.mu.Lock()
		op := s.remoteConfirms.operations[operationID]
		op.State = "completed"
		op.InstallationID = result.ID
		if runErr != nil {
			op.State = "failed"
			op.ErrorCode = "key_operation_denied_or_incomplete"
		}
		s.remoteConfirms.operations[operationID] = op
		s.remoteConfirms.mu.Unlock()
	}()
	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"operation_id": operationID})
}

func (s *Server) remoteKeyOperations(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	out := []remoteKeyStatus{}
	s.remoteConfirms.mu.Lock()
	defer s.remoteConfirms.mu.Unlock()
	for _, op := range s.remoteConfirms.operations {
		if op.OwnerID == sc.OwnerID {
			out = append(out, op)
		}
	}
	return c.JSON(out)
}
