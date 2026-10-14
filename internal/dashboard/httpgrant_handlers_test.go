package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

type fakeGrantManager struct {
	CredentialManager // embedded nil: only the grant paths are exercised here
	grants            []domain.HTTPCredentialGrant
	nextID            int64
}

func (f *fakeGrantManager) GetByID(_ context.Context, id int64) (*domain.Credential, error) {
	return &domain.Credential{ID: id}, nil
}

func (f *fakeGrantManager) SaveHTTPGrant(_ context.Context, g *domain.HTTPCredentialGrant) error {
	f.nextID++
	g.ID = f.nextID
	f.grants = append(f.grants, *g)
	return nil
}
func (f *fakeGrantManager) ListHTTPGrants(_ context.Context, credentialID int64) ([]domain.HTTPCredentialGrant, error) {
	var out []domain.HTTPCredentialGrant
	for i := range f.grants {
		if f.grants[i].CredentialID == credentialID {
			out = append(out, f.grants[i])
		}
	}
	return out, nil
}
func (f *fakeGrantManager) DeleteHTTPGrant(_ context.Context, id int64) error {
	for i := range f.grants {
		if f.grants[i].ID == id {
			f.grants = append(f.grants[:i], f.grants[i+1:]...)
			return nil
		}
	}
	return fiber.ErrNotFound
}

func newGrantTestApp(t *testing.T, role domain.UserRole) (*fiber.App, *fakeGrantManager) {
	t.Helper()
	fm := &fakeGrantManager{}
	s := &Server{
		credentialManager: fm,
		tickets:           newTicketRegistry(),
		logger:            zap.NewNop(),
	}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: "admin1", Role: role})
		return c.Next()
	})
	api := app.Group("/api")
	api.Post("/credentials/:id/http-grants", s.handleCreateHTTPGrant)
	api.Get("/credentials/:id/http-grants", s.handleListHTTPGrants)
	return app, fm
}

func TestCreateHTTPGrantValidation(t *testing.T) {
	app, fm := newGrantTestApp(t, domain.RoleAdmin)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"bad principal", `{"principal_type":"everyone","scheme":"https","host":"api.test"}`, 422},
		{"user without id", `{"principal_type":"user","scheme":"https","host":"api.test"}`, 422},
		{"global without caller", `{"principal_type":"global","scheme":"https","host":"api.test"}`, 422},
		{"bad scheme", `{"principal_type":"user","principal_id":"u1","scheme":"ftp","host":"api.test"}`, 422},
		{"bad host", `{"principal_type":"user","principal_id":"u1","scheme":"https","host":"a b"}`, 422},
		{"bad port", `{"principal_type":"user","principal_id":"u1","scheme":"https","host":"api.test","port":"99999"}`, 422},
		{"bad usage", `{"principal_type":"user","principal_id":"u1","scheme":"https","host":"api.test","usages":["cookie"]}`, 422},
		{"valid user grant", `{"principal_type":"user","principal_id":"u1","scheme":"https","host":"api.test","port":"443"}`, 201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/credentials/7/http-grants", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			res, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.want)
			}
		})
	}
	if len(fm.grants) != 1 {
		t.Fatalf("created %d grants, want 1 (only the valid one)", len(fm.grants))
	}
	g := fm.grants[0]
	if g.PrincipalType != domain.HTTPGrantPrincipalUser || g.Host != "api.test" || g.Port != "443" || g.CreatedBy != "admin1" {
		t.Fatalf("grant = %+v", g)
	}

	// Listing returns the projection.
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/7/http-grants", http.NoBody)
	res, _ := app.Test(req, -1)
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("list status = %d", res.StatusCode)
	}
}

func TestCreateHTTPGrantRequiresAdmin(t *testing.T) {
	app, _ := newGrantTestApp(t, domain.RoleRegular)
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/7/http-grants",
		strings.NewReader(`{"principal_type":"user","principal_id":"u1","scheme":"https","host":"api.test"}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("regular user status = %d, want 403", res.StatusCode)
	}
}
