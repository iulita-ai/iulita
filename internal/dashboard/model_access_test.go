package dashboard

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/models"
	"github.com/iulita-ai/iulita/internal/storage"
	"go.uber.org/zap"
)

type accessUserRepo struct {
	storage.Repository
	user *domain.User
}

func (r *accessUserRepo) GetUser(context.Context, string) (*domain.User, error) { return r.user, nil }
func TestModelsAccessRechecksRoleAndPasswordBeforeWrites(t *testing.T) {
	builds := 0
	m, err := modelruntime.New(newMemConfigRepo(), nil, func(models.Profile, modelruntime.Connection) (llm.Provider, error) { builds++; return nil, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := &accessUserRepo{user: &domain.User{ID: "actor", Role: domain.RoleAdmin}}
	s := &Server{store: repo, modelManager: m, logger: zap.NewNop()}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: "actor", Role: domain.RoleAdmin})
		return c.Next()
	})
	s.registerModelRoutes(app.Group("/api"))
	for _, tc := range []struct {
		role               domain.UserRole
		password           bool
		method, path, body string
		want               int
	}{
		{domain.RoleRegular, false, "GET", "/api/models/catalog", "", 403},
		{domain.RoleAdmin, true, "GET", "/api/models/settings", "", 200},
		{domain.RoleAdmin, true, "POST", "/api/models/profiles/foo/probe", `{}`, 403},
		{domain.RoleAdmin, true, "POST", "/api/models/settings/stage", `{}`, 403},
		{domain.RoleAdmin, false, "POST", "/api/models/settings/stage", `{"settings":{},"private_unknown":"must-never-echo"}`, 422},
	} {
		repo.user.Role = tc.role
		repo.user.MustChangePass = tc.password
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		_ = res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("%s %s: %d %+v", tc.method, tc.path, res.StatusCode, body)
		}
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "must-never-echo") {
			t.Fatal("validation echoes private payload")
		}
	}
	if builds != 0 {
		t.Fatal("readonly access/rejected writes invoked provider factory")
	}
}
func TestModelsRoutesAbsentWithoutAuth(t *testing.T) {
	cs := buildConfigStore(t, t.TempDir())
	m, err := modelruntime.New(newMemConfigRepo(), nil, func(models.Profile, modelruntime.Connection) (llm.Provider, error) { return nil, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{ConfigStore: cs, ModelManager: m, Logger: zap.NewNop(), StaticFS: minimalStaticFS()})
	for _, route := range s.app.GetRoutes() {
		if strings.HasPrefix(route.Path, "/api/models/") {
			t.Fatalf("administrative model route exposed without authentication: %s", route.Path)
		}
	}
}

func TestCredentialWriteGuardRejectsMalformedJSONBeforeManagedConnectionLookup(t *testing.T) {
	cs := buildConfigStore(t, t.TempDir())
	cs.SetModelPolicyManaged()
	s := &Server{configStore: cs}
	app := fiber.New()
	admitted := 0
	app.All("/credentials", s.modelCredentialWrite, func(c *fiber.Ctx) error {
		admitted++
		return c.SendStatus(fiber.StatusNoContent)
	})
	for _, fixture := range []struct {
		method, body string
		want         int
		allow        bool
	}{
		{"POST", `{"name":"models.runtime",`, fiber.StatusBadRequest, false},
		{"POST", `{"consumer_id":42}`, fiber.StatusBadRequest, false},
		{"POST", `{"name":"models.runtime"}`, fiber.StatusConflict, false},
		{"POST", `{"consumer_id":"zai.api_key"}`, fiber.StatusConflict, false},
		{"POST", `{"name":"unrelated.service"}`, fiber.StatusNoContent, true},
		{"DELETE", "", fiber.StatusNoContent, true},
		{"GET", "malformed", fiber.StatusNoContent, true},
	} {
		before := admitted
		res, err := app.Test(httptest.NewRequest(fixture.method, "/credentials", strings.NewReader(fixture.body)))
		if err != nil {
			t.Fatal(err)
		}
		if closeErr := res.Body.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if res.StatusCode != fixture.want || (admitted > before) != fixture.allow {
			t.Fatalf("%s: got status=%d admitted=%v, want status=%d admitted=%v", fixture.method, res.StatusCode, admitted > before, fixture.want, fixture.allow)
		}
	}
}
