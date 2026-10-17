package slackpost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/security"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// SetNativeOwner assigns the bot write connection independently of user-token search.
func (s *PostSkill) SetNativeOwner(owner string) {
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	s.nativeOwner = owner
	s.nativeEpoch++
}

// NativeAvailable requires the explicitly assigned owner connection.
func (s *PostSkill) NativeAvailable(_ context.Context, owner string) bool {
	s.nativeMu.RLock()
	defer s.nativeMu.RUnlock()
	_, bound := s.poster.(BoundChannelPoster)
	return owner != "" && owner == s.nativeOwner && bound
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *PostSkill) NativePolicy() skill.NativePolicy {
	s.nativeMu.RLock()
	defer s.nativeMu.RUnlock()
	return skill.NativePolicy{Revision: fmt.Sprintf("owner-slack-post-v1:%d", s.nativeEpoch), Account: true, Interactive: true, InternalConfirmations: true, Actions: map[string]skill.ActionPolicy{"": {Effect: "mutating", Scopes: []string{"assistant:services:write"}}}}
}

// NativeExecute always confirms the exact final recipient and text through a
// typed security prompt. Ambiguous dispatch errors remain errors, never retries.
func (s *PostSkill) NativeExecute(ctx context.Context, raw json.RawMessage, revision string) (string, error) {
	owner := skill.UserIDFrom(ctx)
	if !s.NativeAvailable(ctx, owner) || s.NativePolicy().Revision != revision {
		return "", errors.New("slack write account unavailable")
	}
	poster, ok := s.poster.(BoundChannelPoster)
	if !ok {
		return "", errors.New("native Slack route unavailable")
	}
	var in postInput
	if json.Unmarshal(raw, &in) != nil || in.Channel == "" || in.Text == "" {
		return "", errors.New("invalid Slack post")
	}
	if found, _ := security.Contains(in.Text + "\n" + in.Provenance); found {
		return `{"status":"rejected","message":"Suspected secret"}`, nil
	}
	if s.poster.WriteMode(ctx, in.Channel) == "off" {
		return `{"status":"rejected","message":"Channel is not writable"}`, nil
	}
	binding := poster.NativePostBinding(ctx, in.Channel)
	if binding == "" {
		return `{"status":"rejected","message":"Posting route unavailable"}`, nil
	}
	preview := fmt.Sprintf("Slack channel %s\n%s", in.Channel, in.Text)
	if len(preview) > 4096 {
		return `{"status":"rejected","message":"Draft exceeds confirmation limit"}`, nil
	}
	confirmer := interact.ConfirmerFrom(ctx)
	if confirmer == nil {
		return `{"status":"rejected","message":"Approval unavailable"}`, nil
	}
	fp := sha256.Sum256(append(append([]byte(nil), raw...), []byte(binding)...))
	decision, err := confirmer.Confirm(ctx, interact.Confirmation{Nonce: uuid.NewString(), OperationID: uuid.NewString(), Fingerprint: hex.EncodeToString(fp[:]), Reason: interact.ConfirmMutation, Preview: preview, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute)}, interact.Actor{UserID: owner})
	if err != nil || !decision.Approved || decision.Actor.UserID != owner {
		return `{"status":"rejected","message":"Draft was not approved; nothing sent"}`, nil //nolint:nilerr // Approval failure proves there was no external dispatch.
	}
	if ctx.Err() != nil || !s.NativeAvailable(ctx, owner) || s.NativePolicy().Revision != revision {
		return "", errors.New("slack write authority changed")
	}
	ts, err := poster.PostBoundToChannel(ctx, in.Channel, in.Text, binding)
	if err != nil {
		return "", errors.New("slack dispatch outcome could not be verified")
	}
	s.writeAudit(ctx, "slack.post.sent", in, "native_approved", true)
	return fmt.Sprintf("Posted to %s (ts %s).", in.Channel, ts), nil
}
