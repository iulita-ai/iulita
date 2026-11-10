package google

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/dayview"
	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func TestDayAllGoogleListsMutationAndReadOnlyRecovery(t *testing.T) {
	var mu sync.Mutex
	status, due := "needsAction", "2026-11-09T00:00:00Z"
	writes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		if strings.HasSuffix(p, "users/@me/lists") {
			if r.URL.Query().Get("pageToken") == "p2" {
				fmt.Fprint(w, `{"items":[{"id":"second"}]}`)
			} else {
				fmt.Fprint(w, `{"items":[{"id":"first"}],"nextPageToken":"p2"}`)
			}
			return
		}
		if strings.HasSuffix(p, "/tasks") {
			if strings.Contains(p, "/first/") {
				fmt.Fprint(w, `{"items":[{"id":"future","title":"future","due":"2026-12-10T00:00:00Z"}]}`)
			} else {
				fmt.Fprintf(w, `{"items":[{"id":"same","title":"due task","etag":"v1","updated":"2026-11-09T10:00:00Z","status":%q,"due":%q}]}`, status, due)
			}
			return
		}
		if strings.HasSuffix(p, "/second/tasks/same") {
			if r.Method == http.MethodPatch {
				if r.Header.Get("If-Match") != "v1" {
					t.Error("missing optimistic precondition")
				}
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if v := body["status"]; v != "" {
					status = v
				}
				if v := body["due"]; v != "" {
					due = v
				}
				writes++
			}
			fmt.Fprintf(w, `{"id":"same","title":"due task","etag":"v1","updated":"2026-11-09T10:00:00Z","status":%q,"due":%q}`, status, due)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, p)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	store := &mockTokenStore{accounts: []domain.GoogleAccount{{ID: 1, UserID: "owner", IsDefault: true, AccountEmail: "one@test", EncryptedAccessToken: "access", EncryptedRefreshToken: "refresh", TokenExpiry: time.Now().Add(time.Hour)}}}
	d := DayTasks{Client: NewClient("id", "secret", "", store, nil, zap.NewNop()), endpoint: srv.URL + "/"}
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	items, e := d.List(ctx, "owner", now, time.UTC)
	if e != nil || len(items) != 1 || items[0].ListID != "second" || !items[0].Reschedule {
		t.Fatalf("items=%+v err=%v", items, e)
	}
	item := items[0]
	target := time.Date(2026, 11, 12, 15, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	if e = d.Change(ctx, "owner", item, "reschedule", target); e != nil {
		t.Fatal(e)
	}
	v, e := d.Verify(ctx, "owner", item, "reschedule", target)
	if e != nil || v != dayview.EffectObserved {
		t.Fatal(v, e)
	}
	if writes != 1 {
		t.Fatal("recovery wrote")
	}
	if due != "2026-11-12T00:00:00Z" {
		t.Fatal("date-only provider changed calendar day", due)
	}
	// Reauthorization invalidates old authority even if the desired state matches.
	store.accounts[0].EncryptedRefreshToken = "new-auth" // gitleaks:allow -- Synthetic credential revision fixture, never used with a live provider.
	v, e = d.Verify(ctx, "owner", item, "reschedule", target)
	if e != nil || v != dayview.Revoked {
		t.Fatal(v, e)
	}
	if e = d.Change(ctx, "owner", item, "complete", time.Time{}); e == nil {
		t.Fatal("reauthorized command accepted")
	}
	if writes != 1 {
		t.Fatal("revoked write")
	}
}
