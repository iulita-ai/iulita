package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
)

var errNativeConnectionChanged = errors.New("slack connection changed")

// Reconnecting persists a new row, even for the same Slack user/team. Exclude
// rotating credentials, expiry and UpdatedAt so ordinary refresh keeps the
// connection identity. No credential-derived value is exposed in capabilities.
func nativeRevision(a *domain.SlackAccount) string {
	if a == nil {
		return "owner-slack-v2:unavailable"
	}
	body, err := json.Marshal(struct {
		ID                        int64
		Owner, Team, User, Scopes string
		Created                   time.Time
	}{a.ID, a.UserID, a.TeamID, a.SlackUserID, a.Scopes, a.CreatedAt})
	if err != nil {
		return "owner-slack-v2:unavailable"
	}
	digest := sha256.Sum256(body)
	return "owner-slack-v2:" + hex.EncodeToString(digest[:])
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *SearchSkill) NativePolicy() skill.NativePolicy {
	// NativeDeclarer has no caller context. Bound this local metadata read and
	// fail closed on lookup failure; dispatch uses its cancellable caller context.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := s.store.GetAnySlackAccount(ctx)
	if err != nil {
		a = nil
	}
	return skill.NativePolicy{Revision: nativeRevision(a), ActionField: "mode", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"search": {Effect: "read", Scopes: []string{"assistant:services:read"}}, "history": {Effect: "read", Scopes: []string{"assistant:services:read"}}, "replies": {Effect: "read", Scopes: []string{"assistant:services:read"}}}}
}

// NativeAvailable requires the explicitly assigned owner connection.
func (s *SearchSkill) NativeAvailable(ctx context.Context, owner string) bool {
	a, err := s.store.GetAnySlackAccount(ctx)
	return err == nil && a != nil && owner != "" && a.UserID == owner
}

// NativeExecute obtains credentials from the exact connection frozen at
// admission, instead of resolving whichever connection exists at dispatch.
func (s *SearchSkill) NativeExecute(ctx context.Context, raw json.RawMessage, revision string) (string, error) {
	owner := skill.UserIDFrom(ctx)
	a, err := s.store.GetAnySlackAccount(ctx)
	if err != nil {
		return "", err
	}
	if a == nil || owner == "" || a.UserID != owner || nativeRevision(a) != revision || s.client == nil {
		return "", errNativeConnectionChanged
	}
	token, err := s.client.userToken(ctx, owner, revision)
	if err != nil {
		return "", err
	}
	// Recheck after token refresh/network wait: the canonical connection may have
	// changed while the credential was being decrypted or refreshed.
	a, err = s.store.GetAnySlackAccount(ctx)
	if err != nil {
		return "", err
	}
	if a == nil || a.UserID != owner || nativeRevision(a) != revision {
		return "", errNativeConnectionChanged
	}
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", err
	}
	skill.MarkSlackSearchUsed(ctx)
	return s.executeResolved(ctx, in, slackClientAdapter{slackapi.New(token, slackapi.OptionHTTPClient(s.client.http))})
}
