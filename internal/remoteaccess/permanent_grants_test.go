package remoteaccess

import (
	"context"
	"testing"
	"time"
)

func existingExecGrant(t *testing.T, s *Service, sc Scope) Grant {
	t.Helper()
	grants := s.Snapshot(sc).Grants
	for id := range grants {
		g := grants[id]
		if g.Capability == "exec" {
			return g
		}
	}
	t.Fatal("missing fixture grant")
	return Grant{}
}

func TestGrantLifetimeRequiresExplicitPermanence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		permanent bool
		expiry    time.Duration
		valid     bool
	}{
		{"missing", false, 0, false}, {"expired", false, -time.Hour, false},
		{"timed", false, time.Hour, true}, {"too long", false, 25 * time.Hour, false},
		{"permanent", true, 0, true}, {"ambiguous future", true, time.Hour, false}, {"ambiguous expired", true, -time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, sc, _, _ := setupService(t)
			g := existingExecGrant(t, s, sc)
			g.Permanent = tc.permanent
			g.ExpiresAt = time.Time{}
			if tc.expiry != 0 {
				g.ExpiresAt = time.Now().Add(tc.expiry)
			}
			_, err := s.PutGrant(sc, g)
			if (err == nil) != tc.valid {
				t.Fatalf("PutGrant error=%v, valid=%v", err, tc.valid)
			}
			if tc.valid && tc.permanent && !g.ActiveAt(time.Now().AddDate(10, 0, 0)) {
				t.Fatal("permanent grant expires")
			}
			g.Enabled = false
			if g.ActiveAt(time.Now()) {
				t.Fatal("disabled grant active")
			}
		})
	}
	// Corrupt or legacy missing lifetime must fail closed at dispatch as well.
	for _, g := range []Grant{{Enabled: true}, {Enabled: true, ExpiresAt: time.Now().Add(-time.Second)}, {Enabled: true, Permanent: true, ExpiresAt: time.Now().Add(time.Hour)}} {
		if g.ActiveAt(time.Now()) {
			t.Fatal("invalid lifetime is active")
		}
	}
}

func TestPermanentGrantPersistsAndKeepsConsentAndBounds(t *testing.T) {
	s, sc, req, tr := setupService(t)
	g := existingExecGrant(t, s, sc)
	g.Permanent = true
	g.ExpiresAt = time.Time{}
	if _, err := s.PutGrant(sc, g); err != nil {
		t.Fatal(err)
	}
	path, vault := s.path, s.vault
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := NewService(path, vault, tr)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	g = existingExecGrant(t, again, sc)
	if !g.Permanent || !g.ExpiresAt.IsZero() || !g.ActiveAt(time.Now().AddDate(10, 0, 0)) {
		t.Fatal("permanent lifetime lost on reload")
	}
	if _, err = again.Preview(Scope{"other", sc.CallerID}, req); err == nil {
		t.Fatal("cross owner grant")
	}
	if _, err = again.Preview(Scope{sc.OwnerID, "other"}, req); err == nil {
		t.Fatal("cross caller grant")
	}
	if _, err = again.Execute(context.Background(), sc, req, nil); err == nil || tr.calls != 0 {
		t.Fatal("permanent grant bypassed consent")
	}
	tr.run = func(ctx context.Context) (TransportResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.After(time.Now()) || deadline.After(time.Now().Add(6*time.Minute)) {
			t.Error("permanent grant removed operation timeout")
		}
		code := 0
		return TransportResult{Dispatched: true, ExitCode: &code}, nil
	}
	x, err := again.Execute(context.Background(), sc, req, func(_ context.Context, a Approval) (bool, error) {
		if !a.AuthorizationExpiresAt.IsZero() {
			t.Error("unexpected grant deadline")
		}
		if !a.ExpiresAt.After(time.Now()) || a.ExpiresAt.After(time.Now().Add(5*time.Minute)) {
			t.Error("consent lifetime widened")
		}
		return true, nil
	})
	if err != nil || x.State != "completed" || tr.calls != 1 {
		t.Fatalf("execution %+v %v calls%d", x, err, tr.calls)
	}
	g.Enabled = false
	if _, err = again.PutGrant(sc, g); err != nil {
		t.Fatal(err)
	}
	if _, err = again.Preview(sc, req); err == nil {
		t.Fatal("revoked permanent grant admitted")
	}
}

func TestPermanentGrantRespectsIdentityExpiry(t *testing.T) {
	s, sc, req, tr := setupService(t)
	g := existingExecGrant(t, s, sc)
	g.Permanent = true
	g.ExpiresAt = time.Time{}
	if _, err := s.PutGrant(sc, g); err != nil {
		t.Fatal(err)
	}
	identity := s.Snapshot(sc).Identities[s.Snapshot(sc).Routes[req.RouteID].Hops[0].IdentityID]
	expiry := time.Now().Add(time.Minute)
	identity.ExpiresAt = expiry
	if _, err := s.PutIdentity(context.Background(), sc, identity, nil); err != nil {
		t.Fatal(err)
	}
	a, err := s.Preview(sc, req)
	if err != nil || !a.AuthorizationExpiresAt.Equal(expiry) {
		t.Fatalf("identity expiry ignored: %+v %v", a, err)
	}
	tr.run = func(ctx context.Context) (TransportResult, error) {
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(expiry) {
			t.Error("identity expiry not applied to operation")
		}
		code := 0
		return TransportResult{Dispatched: true, ExitCode: &code}, nil
	}
	if x, runErr := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) { return true, nil }); runErr != nil || x.State != "completed" {
		t.Fatalf("%+v %v", x, runErr)
	}
	identity.ExpiresAt = time.Now().Add(-time.Second)
	if _, err = s.PutIdentity(context.Background(), sc, identity, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Preview(sc, req); err == nil {
		t.Fatal("expired identity authorized")
	}
}

func TestPermanentGrantChangeInvalidatesPendingConsent(t *testing.T) {
	for _, mode := range []string{"revoke", "route revision", "switch to timed"} {
		t.Run(mode, func(t *testing.T) {
			s, sc, req, tr := setupService(t)
			g := existingExecGrant(t, s, sc)
			g.Permanent = true
			g.ExpiresAt = time.Time{}
			if _, err := s.PutGrant(sc, g); err != nil {
				t.Fatal(err)
			}
			x, _ := s.Execute(context.Background(), sc, req, func(context.Context, Approval) (bool, error) {
				switch mode {
				case "route revision":
					r := s.Snapshot(sc).Routes[req.RouteID]
					if _, err := s.PutRoute(sc, r); err != nil {
						t.Fatal(err)
					}
				case "revoke":
					g.Enabled = false
					if _, err := s.PutGrant(sc, g); err != nil {
						t.Fatal(err)
					}
				default:
					g.Permanent = false
					g.ExpiresAt = time.Now().Add(time.Hour)
					if _, err := s.PutGrant(sc, g); err != nil {
						t.Fatal(err)
					}
				}
				return true, nil
			})
			if tr.calls != 0 || x.State != "cancelled" {
				t.Fatalf("changed consent dispatched: %+v", x)
			}
		})
	}
}

func TestPermanentRunbookGrantStillConsumesUseLimit(t *testing.T) {
	s, sc, req, tr := setupService(t)
	b, err := s.PutRunbook(sc, Runbook{RouteID: req.RouteID, Command: "true", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutGrant(sc, Grant{RouteID: req.RouteID, CallerID: "scheduler", Capability: "runbook", RunbookID: b.ID, MaxUses: 1, Enabled: true, Permanent: true}); err != nil {
		t.Fatal(err)
	}
	x, err := s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-100", RunbookRequest{RunbookID: b.ID})
	if err != nil || x.State != "completed" {
		t.Fatalf("%+v %v", x, err)
	}
	if _, err = s.ExecuteScheduledRunbook(context.Background(), sc.OwnerID, "job-1-task-101", RunbookRequest{RunbookID: b.ID}); err == nil || tr.calls != 1 {
		t.Fatal("permanent runbook bypassed use limit")
	}
}
