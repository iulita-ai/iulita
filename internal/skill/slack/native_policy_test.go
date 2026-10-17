package slack

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
)

func TestNativeSlackConnectionRevisionTracksReconnectNotTokenRefresh(t *testing.T) {
	a := &domain.SlackAccount{ID: 1, UserID: "owner", SlackUserID: "U_A", TeamID: "T_A", Scopes: `["search:read"]`, EncryptedAccessToken: "old", CreatedAt: time.Now()}
	s := newTestSkill(nil, fakeOwnerStore{account: a})
	before := s.NativePolicy().Revision
	a.EncryptedAccessToken = "new"
	a.EncryptedRefreshToken = "refresh"
	a.TokenExpiry = time.Now().Add(time.Hour)
	a.UpdatedAt = time.Now()
	if s.NativePolicy().Revision != before {
		t.Fatal("routine token refresh invalidated the connection identity")
	}
	a.TeamID, a.SlackUserID = "T_B", "U_B"
	if !s.NativeAvailable(context.Background(), "owner") || s.NativePolicy().Revision == before {
		t.Fatal("same-owner workspace reconnect retained the admitted binding")
	}
}

func TestNativeSlackPinsTheCredentialSourceRow(t *testing.T) {
	// The canonical owner lookup can pass immediately before the token source
	// starts returning a replacement. The token lookup must independently pin
	// the loaded row, rather than using an unqualified GetUserClient call.
	original := &domain.SlackAccount{ID: 1, UserID: "owner", SlackUserID: "U_A", TeamID: "T_A", EncryptedAccessToken: "old"}
	replacement := &domain.SlackAccount{ID: 2, UserID: "owner", SlackUserID: "U_B", TeamID: "T_B", EncryptedAccessToken: "new"}
	s := newTestSkill(nil, fakeOwnerStore{account: original})
	s.client = newClient(&fakeStore{account: replacement}, nil)
	ctx := skill.WithUserID(context.Background(), "owner")
	_, err := s.NativeExecute(ctx, json.RawMessage(`{"mode":"search","query":"fixture"}`), s.NativePolicy().Revision)
	if !errors.Is(err, errNativeConnectionChanged) {
		t.Fatal("replacement credential source was not rejected", err)
	}
}
