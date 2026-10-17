package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// turnInteraction uses durable typed prompts; ordinary chat messages can never
// approve an action. A resumed/revoked turn cannot release a pending skill.
type turnInteraction struct {
	e *Engine
	f conversations.Fence
	a Authority
}

func (i turnInteraction) wait(ctx context.Context, p conversations.Prompt) (conversations.Prompt, error) {
	if i.a.PrincipalType == "service" || i.a.OwnerID != i.f.OwnerID || i.a.DeviceID == "" || i.e.VerifyAuthority == nil {
		return p, interact.ErrNoPrompter
	}
	p.ExpectedDevice = i.a.DeviceID
	p.ExpectedFamily = i.a.FamilyID
	p.RequiredScopes = append([]string(nil), i.a.Scopes...)
	expiry := time.Now().Add(interact.DefaultTimeout)
	if p.ExpiresAt.IsZero() || p.ExpiresAt.After(expiry) {
		p.ExpiresAt = expiry
	}
	if d, ok := ctx.Deadline(); ok && d.Before(p.ExpiresAt) {
		p.ExpiresAt = d
	}
	p, err := i.e.Repo.OpenPrompt(ctx, i.f, p)
	if err != nil {
		return p, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-ticker.C:
			if checkErr := i.e.Repo.CheckFence(ctx, i.f); checkErr != nil {
				return p, checkErr
			}
			p, err = i.e.Repo.Prompt(ctx, i.f.OwnerID, i.f.ConversationID, i.f.TurnID, p.ID)
			if err != nil {
				return p, err
			}
			if p.State == "open" {
				continue
			}
			if p.State != "answered" || p.Approver == nil {
				return p, interact.ErrConfirmRejected
			}
			if p.Approver.FamilyID != i.a.FamilyID || p.Approver.DeviceID != i.a.DeviceID || i.e.VerifyAuthority(ctx, Authority(*p.Approver)) != nil {
				return p, conversations.ErrApproval
			}
			return p, nil
		}
	}
}

// Ask waits for an authenticated typed clarification response.
func (i turnInteraction) Ask(ctx context.Context, q string, options []interact.Option) (string, error) {
	p := conversations.Prompt{Kind: "choice", Question: q, AllowText: true}
	for _, o := range options {
		p.Choices = append(p.Choices, conversations.Choice{ID: o.ID, Label: o.Label})
	}
	p, err := i.wait(ctx, p)
	return p.Decision, err
}

// Confirm binds a typed approval to the complete server-issued operation.
func (i turnInteraction) Confirm(ctx context.Context, action interact.Confirmation, expect interact.Actor) (interact.Decision, error) {
	if expect.UserID != i.f.OwnerID || (expect.ChannelInstance != "" && expect.ChannelInstance != "mobile:"+i.f.ConversationID) || action.Nonce == "" || action.OperationID == "" || action.Fingerprint == "" || action.MaxBytes < 0 {
		return interact.Decision{}, conversations.ErrApproval
	}
	// Bind the complete engine-issued tuple, never model input. The public prompt
	// gets its own nonce; the HTTP engine's nonce stays in this server closure.
	tuple, err := json.Marshal(action)
	if err != nil {
		return interact.Decision{}, err
	}
	fp := sha256.Sum256(tuple)
	p, err := i.wait(ctx, conversations.Prompt{Kind: "confirmation", OperationID: uuid.NewString(), Question: action.Reason, Preview: action.Preview, Fingerprint: fp[:], ExpiresAt: action.ExpiresAt, MaxBytes: action.MaxBytes})
	if err != nil {
		return interact.Decision{}, err
	}
	if p.Decision != "approve" {
		return interact.Decision{}, interact.ErrConfirmRejected
	}
	if checkErr := i.e.Repo.PhoneConsent(ctx, i.f, p.OperationID, fp[:], func(a conversations.ApprovalAuthority) error { return i.e.VerifyAuthority(ctx, Authority(a)) }); checkErr != nil {
		return interact.Decision{}, checkErr
	}
	return interact.Decision{Approved: true, Actor: interact.Actor{UserID: i.f.OwnerID, Channel: "mobile", ChannelInstance: "mobile:" + i.f.ConversationID, ExternalID: i.a.DeviceID}, MaxBytes: p.GrantedMaxBytes}, nil
}

var _ interact.PromptAsker = turnInteraction{}
var _ interact.ConfirmationAsker = turnInteraction{}
