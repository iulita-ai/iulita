package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
)

const businessCandidateTag = "_iulita_process_v1"

type businessCandidateReceipt struct {
	UpdateID int64                 `json:"update_id"`
	Process  *businessCandidateDTO `json:"_iulita_process_v1"`
}
type businessCandidateDTO struct {
	Scope            business.Scope `json:"scope"`
	ExpectedRevision uint64         `json:"expected_revision"`
	MessageID        int            `json:"message_id"`
	MessageRevision  int64          `json:"message_revision"`
	Generation       string         `json:"generation"`
}

func hasBusinessCandidateTag(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	_, ok := fields[businessCandidateTag]
	return ok
}
func encodeBusinessCandidate(candidate businessCandidate) (business.PollEntry, error) {
	raw, e := json.Marshal(businessCandidateReceipt{UpdateID: candidate.updateID, Process: &businessCandidateDTO{Scope: candidate.scope, ExpectedRevision: candidate.expectedRevision, MessageID: candidate.messageID, MessageRevision: candidate.revision, Generation: candidate.generation}})
	return business.PollEntry{UpdateID: candidate.updateID, Payload: raw}, e
}
func decodeBusinessCandidate(entry business.PollEntry) (businessCandidate, bool, error) {
	if !hasBusinessCandidateTag(entry.Payload) {
		return businessCandidate{}, false, nil
	}
	var value businessCandidateReceipt
	decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
	decoder.DisallowUnknownFields()
	if len(entry.Payload) > 8192 || decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF || value.Process == nil || value.UpdateID != entry.UpdateID || entry.UpdateID <= 0 {
		return businessCandidate{}, true, business.ErrDenied
	}
	p := value.Process
	if p.Scope.Key.Validate() != nil || p.Scope.OwnerID == "" || len(p.Scope.OwnerID) > 128 || p.Scope.PeerID <= 0 || p.ExpectedRevision == 0 || p.MessageID <= 0 || p.MessageRevision < 0 || p.Generation == "" || len(p.Generation) > 128 {
		return businessCandidate{}, true, business.ErrDenied
	}
	return businessCandidate{scope: p.Scope, expectedRevision: p.ExpectedRevision, messageID: p.MessageID, revision: p.MessageRevision, generation: p.Generation, updateID: entry.UpdateID}, true, nil
}
func terminalBusinessCandidate(err error) bool {
	return errors.Is(err, business.ErrDenied) || errors.Is(err, business.ErrStale) || errors.Is(err, business.ErrUnsupported) || errors.Is(err, business.ErrQuota)
}

// drainBusinessCandidate handles only authenticated local queue metadata. It is
// never called for live wire envelopes; the reserved marker is rejected there.
func (c *Channel) drainBusinessCandidate(ctx context.Context, entry business.PollEntry) (bool, error) {
	candidate, handled, err := decodeBusinessCandidate(entry)
	if !handled {
		return false, nil
	}
	ack := func() (bool, error) {
		return true, c.businessPoll.AckTelegramBusinessPoll(ctx, c.instanceID, c.botID, entry.UpdateID)
	}
	if err != nil || candidate.scope.Key.InstanceID != c.instanceID || candidate.scope.Key.BotID != c.botID || candidate.generation != c.businessDeliveryGeneration() || c.business == nil || c.testEnvironment {
		return ack()
	}
	if err = c.business.AdmitProcessCandidate(ctx, candidate.scope, candidate.messageID, candidate.revision, candidate.generation, candidate.expectedRevision); err != nil {
		if terminalBusinessCandidate(err) {
			return ack()
		}
		if errors.Is(err, business.ErrUnavailable) {
			metadata, readErr := c.business.GetState(ctx, candidate.scope.OwnerID, candidate.scope.Key)
			if readErr == nil && (!metadata.Connection.Enabled || metadata.Connection.Paused || metadata.Connection.Quarantined || metadata.Connection.NeedsRefresh) {
				return ack()
			}
		}
		return true, nil // Transient DB failure: retain the durable receipt, no model.
	}
	c.scheduleBusinessCandidate(ctx, candidate) // A full queue leaves the receipt pending.
	return true, nil
}
