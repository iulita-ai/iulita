package google

import (
	"context"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func TestDayConnectionPinsAccountAndRejectsForeignID(t *testing.T) {
	store := &mockTokenStore{accounts: []domain.GoogleAccount{{ID: 1, UserID: "owner", IsDefault: true, AccountEmail: "one@test", EncryptedAccessToken: "one", EncryptedRefreshToken: "refresh1", TokenExpiry: time.Now().Add(time.Hour)}, {ID: 2, UserID: "other", AccountEmail: "two@test", EncryptedAccessToken: "two", TokenExpiry: time.Now().Add(time.Hour)}}}
	d := DayTasks{Client: NewClient("id", "secret", "", store, nil, zap.NewNop())}
	_, a, e := d.service(context.Background(), "owner", "1")
	if e != nil || a.ID != 1 {
		t.Fatalf("pin %v %v", a, e)
	}
	if _, _, e = d.service(context.Background(), "owner", "2"); e == nil {
		t.Fatal("foreign account accepted")
	}
	item := dayview.Item{Revision: "item-v1"}
	before := googleConnectionRevision(a, item)
	a.EncryptedRefreshToken = "reauthorized" // gitleaks:allow -- Synthetic credential revision fixture, never used with a live provider.
	if before == googleConnectionRevision(a, item) {
		t.Fatal("credential replacement did not invalidate approval")
	}
	if _, _, e = d.service(context.Background(), "owner", "not-an-id"); e == nil {
		t.Fatal("invalid connection accepted")
	}
}
