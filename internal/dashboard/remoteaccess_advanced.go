package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
)

func remoteOperationBody(c *fiber.Ctx, v any) error {
	if len(c.Body()) > 16<<20 {
		return fiber.ErrRequestEntityTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(c.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(v) != nil {
		return fiber.ErrBadRequest
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fiber.ErrBadRequest
	}
	return nil
}
func (s *Server) remotePutRunbook(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	if auth.GetClaims(c).Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	var b remoteaccess.Runbook
	if bodyErr := remoteBody(c, &b); bodyErr != nil {
		return bodyErr
	}
	// Catalog IDs must not retain Fiber's reusable request buffer.
	b.ID = strings.Clone(c.Params("id"))
	saved, err := s.remoteAccess.PutRunbook(sc, b)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(saved)
}
func (s *Server) remoteRunbook(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	sc.CallerID = "main"
	var r remoteaccess.RunbookRequest
	if bodyErr := remoteBody(c, &r); bodyErr != nil {
		return bodyErr
	}
	x, err := s.remoteAccess.ExecuteRunbook(c.UserContext(), sc, r)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(x)
}
func (s *Server) remoteOperationPreview(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var r remoteaccess.ExecuteRequest
	if bodyErr := remoteOperationBody(c, &r); bodyErr != nil {
		return bodyErr
	}
	approval, err := s.remoteAccess.PreviewOperation(sc, r)
	if err != nil {
		return remoteError(err)
	}
	return c.JSON(approval)
}
func (s *Server) remoteOperationExecute(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	var r remoteaccess.ExecuteRequest
	if bodyErr := remoteOperationBody(c, &r); bodyErr != nil {
		return bodyErr
	}
	if r.ApprovalID == "" || r.ExpectedPlanHash == "" {
		return fiber.ErrBadRequest
	}
	x, err := s.remoteAccess.ExecuteOperation(c.UserContext(), sc, r, func(_ context.Context, a remoteaccess.Approval) (bool, error) {
		return a.PlanHash == r.ExpectedPlanHash, nil
	})
	if err != nil && x.ID == "" {
		return remoteError(err)
	}
	return c.JSON(x)
}

// SSH schedules use the existing durable user-owned AgentJob scheduler. The
// scheduled runner only sees remote_runbook and still needs an exact standing grant.
func (s *Server) remoteSchedule(c *fiber.Ctx) error {
	sc, err := remoteScope(c)
	if err != nil {
		return err
	}
	if auth.GetClaims(c).Role != domain.RoleAdmin {
		return fiber.ErrForbidden
	}
	var in struct {
		Name     string                      `json:"name"`
		Interval string                      `json:"interval"`
		Request  remoteaccess.RunbookRequest `json:"request"`
	}
	if bodyErr := remoteBody(c, &in); bodyErr != nil {
		return bodyErr
	}
	interval, parseErr := time.ParseDuration(in.Interval)
	if parseErr != nil || interval < time.Minute || interval > 365*24*time.Hour || in.Name == "" {
		return fiber.ErrBadRequest
	}
	snapshot := s.remoteAccess.Snapshot(sc)
	book, ok := snapshot.Runbooks[in.Request.RunbookID]
	if !ok || !book.Enabled {
		return fiber.ErrForbidden
	}
	authorized := false
	for id := range snapshot.Grants {
		g := snapshot.Grants[id]
		if g.Enabled && g.CallerID == "scheduler" && g.RouteID == book.RouteID && g.RunbookID == book.ID && g.RunbookRevision == book.Revision && g.Capability == "runbook" && g.ExpiresAt.After(time.Now()) && g.MaxUses > g.Uses {
			authorized = true
			break
		}
	}
	if !authorized {
		return fiber.NewError(403, "Create an exact scheduler runbook grant first")
	}
	payload, marshalErr := json.Marshal(in.Request)
	if marshalErr != nil {
		return fiber.ErrBadRequest
	}
	job := &domain.AgentJob{UserID: sc.OwnerID, Name: in.Name, Prompt: "iulita-ssh-runbook-v1:" + string(payload), Interval: in.Interval, Enabled: true, DeliveryChatID: "web:" + sc.OwnerID}
	if err = s.store.CreateAgentJob(c.UserContext(), job); err != nil {
		return remoteError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(job)
}
