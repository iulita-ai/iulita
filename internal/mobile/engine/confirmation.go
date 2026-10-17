package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill"
)

// confirmTool runs BEFORE StartTool. Denial/timeout never marks a mutation as
// dispatched. Only the typed RPC with a verified OAuth actor can approve it.
func (e *Engine) confirmTool(ctx context.Context, f conversations.Fence, t Tool, a skill.ActionPolicy, payload []byte) (func(context.Context) error, error) {
	authority := AuthorityFrom(ctx)
	if authority.PrincipalType == "service" || authority.OwnerID != f.OwnerID || authority.DeviceID == "" || authority.FamilyID == "" {
		return nil, conversations.ErrApproval
	}
	preview := string(payload)
	if len(preview) > 4000 {
		return nil, conversations.ErrInvalid
	} // Never confirm a truncated mutation.
	if !utf8.ValidString(preview) {
		return nil, conversations.ErrInvalid
	}
	fingerprint := sha256.Sum256(payload)
	expires := time.Now().Add(90 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	p, err := e.Repo.OpenPrompt(ctx, f, conversations.Prompt{Kind: "confirmation", OperationID: uuid.NewString(), Question: "Подтвердить действие «" + t.Definition.Name + "»?", Preview: preview, Fingerprint: fingerprint[:], ExpiresAt: expires, RequiredScopes: append(append([]string(nil), t.RequiredScopes...), a.Scopes...)})
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			if e.Repo.CheckFence(ctx, f) != nil {
				return nil, conversations.ErrFenced
			}
			current, err := e.Repo.Prompt(ctx, f.OwnerID, f.ConversationID, f.TurnID, p.ID)
			if err != nil {
				return nil, err
			}
			switch current.State {
			case "open":
				continue
			case "answered":
				if current.Decision != "approve" || current.Approver == nil || current.Approver.OwnerID != f.OwnerID || e.VerifyAuthority == nil {
					return nil, conversations.ErrApproval
				}
				check := func(ctx context.Context) error {
					return e.Repo.PhoneConsent(ctx, f, p.OperationID, fingerprint[:], func(a conversations.ApprovalAuthority) error { return e.VerifyAuthority(ctx, Authority(a)) })
				}
				if verifyErr := check(ctx); verifyErr != nil {
					return nil, verifyErr
				}
				return check, nil
			default:
				return nil, errors.New("native action was not approved")
			}
		}
	}
}
