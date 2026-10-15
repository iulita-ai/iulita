package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrApproval rejects missing or insufficient action approval.
var ErrApproval = errors.New("mobile approval authority required")

// PhoneConsent is an exact per-task content/system-operation consent. It does
// not grant native permissions, Accessibility authority or provider delivery.
// Caller holds the account guard across this read and its task DB commit.
func (r *Repository) PhoneConsent(ctx context.Context, f Fence, operation string, fingerprint []byte, verify func(ApprovalAuthority) error) error {
	var body []byte
	var actor string
	err := r.db.QueryRowContext(ctx, `SELECT body,actor_device FROM mobile_prompts WHERE owner_id=? AND conversation_id=? AND turn_id=? AND generation=? AND processing_epoch=? AND kind='confirmation' AND operation_id=? AND fingerprint=? AND state='answered' AND decision='approve' AND actor_device<>'' AND expires_at>?`, f.OwnerID, f.ConversationID, f.TurnID, f.Generation, f.ProcessingEpoch, operation, fingerprint, time.Now().UnixMilli()).Scan(&body, &actor)
	if err != nil {
		return ErrFenced
	}
	var p Prompt
	if json.Unmarshal(body, &p) != nil || p.Approver == nil || p.Approver.DeviceID != actor || !approvalPermits(p, *p.Approver) || verify == nil || verify(*p.Approver) != nil {
		return ErrFenced
	}
	return nil
}
