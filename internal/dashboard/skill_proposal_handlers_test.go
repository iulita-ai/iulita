package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// buildProposalServer wires a dashboard Server with a real store (for proposal
// persistence), an auth service, and the given skill manager. Returns the store
// so tests can seed proposals.
func buildProposalServer(t *testing.T, mgr ExternalSkillManager) (*Server, *sqlite.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RunMigrations(context.Background()); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("test-pass")
	for _, u := range []*domain.User{
		{ID: "admin-1", Username: "admin", Role: "admin", PasswordHash: hash},
		{ID: "user-1", Username: "user", Role: "user", PasswordHash: hash},
	} {
		if err := store.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	authSvc := auth.NewService(store, testJWTSecret, time.Hour, 24*time.Hour)
	srv := New(Config{
		Address:      ":0",
		Logger:       zap.NewNop(),
		StaticFS:     fstest.MapFS{"index.html": {Data: []byte("ok")}},
		AuthService:  authSvc,
		SkillManager: mgr,
		Store:        store,
	})
	return srv, store
}

func roleToken(t *testing.T, userID, role string) string {
	t.Helper()
	now := time.Now()
	claims := auth.Claims{
		UserID: userID, Username: role, Role: domain.UserRole(role),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			Subject:   userID,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func doProposalReq(t *testing.T, srv *Server, method, path, token string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

func seedProposal(t *testing.T, store *sqlite.Store, status string) *domain.SkillProposal {
	t.Helper()
	p := &domain.SkillProposal{
		ChatID: "chat1", UserID: "user-1", Slug: "deploy-checklist",
		Name: "Deploy Checklist", Body: "Run health checks.", Triggers: "deploy,rollout",
		Status: status,
	}
	if err := store.SaveSkillProposal(context.Background(), p); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	return p
}

func TestInstallProposal_PendingSucceeds(t *testing.T) {
	mgr := &mockExtSkillMgr{installed: &domain.InstalledSkill{Slug: "deploy-checklist"}}
	srv, store := buildProposalServer(t, mgr)
	p := seedProposal(t, store, domain.SkillProposalPending)

	code := doProposalReq(t, srv, http.MethodPost,
		"/api/skills/proposals/"+itoa(p.ID)+"/install", roleToken(t, "admin-1", "admin"))
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if len(mgr.installCalls) != 1 || mgr.installCalls[0].ref != "deploy-checklist" {
		t.Errorf("InstallAuthored not called with slug: %+v", mgr.installCalls)
	}
	got, _ := store.GetSkillProposal(context.Background(), p.ID)
	if got.Status != domain.SkillProposalInstalled {
		t.Errorf("expected status installed, got %q", got.Status)
	}
}

func TestInstallProposal_RejectedIsBlocked(t *testing.T) {
	mgr := &mockExtSkillMgr{installed: &domain.InstalledSkill{Slug: "evil"}}
	srv, store := buildProposalServer(t, mgr)
	p := seedProposal(t, store, domain.SkillProposalRejected)

	code := doProposalReq(t, srv, http.MethodPost,
		"/api/skills/proposals/"+itoa(p.ID)+"/install", roleToken(t, "admin-1", "admin"))
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for rejected proposal, got %d", code)
	}
	if len(mgr.installCalls) != 0 {
		t.Error("InstallAuthored must NOT be called for a rejected proposal")
	}
	got, _ := store.GetSkillProposal(context.Background(), p.ID)
	if got.Status != domain.SkillProposalRejected {
		t.Errorf("rejected proposal status changed to %q", got.Status)
	}
}

func TestInstallProposal_NotFound(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv, _ := buildProposalServer(t, mgr)
	code := doProposalReq(t, srv, http.MethodPost,
		"/api/skills/proposals/9999/install", roleToken(t, "admin-1", "admin"))
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestInstallProposal_NonAdminForbidden(t *testing.T) {
	mgr := &mockExtSkillMgr{installed: &domain.InstalledSkill{Slug: "deploy-checklist"}}
	srv, store := buildProposalServer(t, mgr)
	p := seedProposal(t, store, domain.SkillProposalPending)

	code := doProposalReq(t, srv, http.MethodPost,
		"/api/skills/proposals/"+itoa(p.ID)+"/install", roleToken(t, "user-1", "user"))
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", code)
	}
	if len(mgr.installCalls) != 0 {
		t.Error("non-admin must not reach InstallAuthored")
	}
}

func TestDiscardProposal(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv, store := buildProposalServer(t, mgr)
	p := seedProposal(t, store, domain.SkillProposalPending)

	code := doProposalReq(t, srv, http.MethodDelete,
		"/api/skills/proposals/"+itoa(p.ID), roleToken(t, "admin-1", "admin"))
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	got, _ := store.GetSkillProposal(context.Background(), p.ID)
	if got.Status != domain.SkillProposalDiscarded {
		t.Errorf("expected discarded, got %q", got.Status)
	}
}

func TestListProposals_NonAdminForbidden(t *testing.T) {
	mgr := &mockExtSkillMgr{}
	srv, _ := buildProposalServer(t, mgr)
	code := doProposalReq(t, srv, http.MethodGet,
		"/api/skills/proposals", roleToken(t, "user-1", "user"))
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin list, got %d", code)
	}
}
