package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/knadh/koanf/v2"
	"go.uber.org/zap"
)

type dashboardTranslation struct{}

func (*dashboardTranslation) Name() string                 { return "translate" }
func (*dashboardTranslation) Description() string          { return "Translation" }
func (*dashboardTranslation) InputSchema() json.RawMessage { return json.RawMessage(`{}`) }
func (*dashboardTranslation) Execute(context.Context, json.RawMessage) (string, error) {
	panic("status must not invoke translation")
}
func (*dashboardTranslation) DefaultEnabled() bool { return false }
func (*dashboardTranslation) ServiceStatus() skill.ServiceStatus {
	return skill.ServiceStatus{Provider: "zai", Service: "general_translation", State: "ready"}
}

type translationReload struct {
	cs  *config.Store
	reg *skill.Registry
}

func (p translationReload) PublishConfigChanged(_ context.Context, key string) {
	v, ok := p.cs.GetEffective(key)
	p.reg.DispatchConfigChanged(key, v, !ok)
}
func translationRegistry(cs *config.Store) *skill.Registry {
	reg := skill.NewRegistry()
	reg.RegisterWithManifest(&dashboardTranslation{}, &skill.Manifest{Name: "translate", ConfigKeys: []string{"skills.translate.enabled"}})
	cs.SetPublisher(translationReload{cs, reg})
	if v, ok := cs.GetEffective("skills.translate.enabled"); ok {
		reg.DispatchConfigChanged("skills.translate.enabled", v, false)
	}
	return reg
}
func translationServer(cs *config.Store, reg *skill.Registry) *Server {
	s := &Server{configStore: cs, registry: reg, logger: zap.NewNop(), app: fiber.New()}
	s.app.Use(func(c *fiber.Ctx) error {
		if role := c.Get("X-Test-Role"); role != "" {
			c.Locals(auth.ContextKeyUser, &auth.Claims{Username: role, Role: domain.UserRole(role)})
		}
		return c.Next()
	})
	s.app.Put("/api/skills/:name/toggle", s.handleToggleSkill)
	s.app.Put("/api/skills/:name/config/:key", s.handleSetSkillConfig)
	s.app.Get("/api/skills/:name/config", s.handleGetSkillConfig)
	s.app.Get("/api/skills", s.handleSkills)
	s.app.Put("/api/config/:key", s.handleSetConfig)
	s.app.Delete("/api/config/:key", s.handleDeleteConfig)
	return s
}
func translationReq(t *testing.T, s *Server, method, path, role, body string) (status int, response string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Test-Role", role)
	resp, err := s.app.Test(r, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}
func TestTranslationEnabledWritesRequireAdmin(t *testing.T) {
	cs := buildConfigStore(t, "")
	s := translationServer(cs, translationRegistry(cs))
	for _, role := range []string{"", "user"} {
		for _, r := range []struct{ method, path, body string }{
			{"PUT", "/api/skills/translate/toggle", `{"enabled":true}`},
			{"PUT", "/api/skills/translate/config/skills.translate.enabled", `{"value":"true"}`},
			{"PUT", "/api/config/skills.translate.enabled", `{"value":"true"}`},
			{"DELETE", "/api/config/skills.translate.enabled", ``},
		} {
			code, body := translationReq(t, s, r.method, r.path, role, r.body)
			if code != 403 {
				t.Fatalf("%s %s role%q: %d %s", r.method, r.path, role, code, body)
			}
		}
	}
	if cs.HasOverride("skills.translate.enabled") {
		t.Fatal("non-admin wrote config")
	}
	for _, path := range []string{"/api/skills", "/api/skills/translate/config"} {
		code, body := translationReq(t, s, "GET", path, "user", "")
		if code != 200 || !strings.Contains(body, `"service_status"`) || !strings.Contains(body, `"checked":false`) {
			t.Fatalf("safe status unavailable %d %s", code, body)
		}
		for _, secret := range []string{"api_key", "generation", "fingerprint"} {
			if strings.Contains(body, secret) {
				t.Fatalf("status leaked %s", secret)
			}
		}
	}
}
func TestTranslationOptInSurvivesRestartAndReset(t *testing.T) {
	repo := newMemConfigRepo()
	k := koanf.New(".")
	_ = k.Set("skills.translate.enabled", false)
	newStore := func() *config.Store {
		cs := config.NewStore(&config.Config{}, k, repo, nil, zap.NewNop())
		if err := cs.LoadOverrides(context.Background()); err != nil {
			t.Fatal(err)
		}
		return cs
	}
	cs := newStore()
	reg := translationRegistry(cs)
	if !reg.IsDisabled("translate") {
		t.Fatal("must start disabled")
	}
	s := translationServer(cs, reg)
	for _, enabled := range []bool{true, false, true} {
		body, _ := json.Marshal(map[string]bool{"enabled": enabled})
		code, out := translationReq(t, s, "PUT", "/api/skills/translate/toggle", "admin", string(body))
		if code != 200 {
			t.Fatalf("toggle %d %s", code, out)
		}
		if v, ok := cs.Get("skills.translate.enabled"); !ok || v != map[bool]string{true: "true", false: "false"}[enabled] {
			t.Fatalf("explicit override %q %v", v, ok)
		}
		cs = newStore()
		reg = translationRegistry(cs)
		s = translationServer(cs, reg)
		if reg.IsDisabled("translate") == enabled {
			t.Fatal("restart lost enabled override")
		}
	}
	code, out := translationReq(t, s, "DELETE", "/api/config/skills.translate.enabled", "admin", "")
	if code != 200 {
		t.Fatalf("reset %d %s", code, out)
	}
	if !reg.IsDisabled("translate") {
		t.Fatal("reset failed to restore false baseline")
	}
	cs = newStore()
	if !translationRegistry(cs).IsDisabled("translate") {
		t.Fatal("reset lost after restart")
	}
	code, _ = translationReq(t, s, "PUT", "/api/skills/translate/config/skills.translate.enabled", "admin", `{"value":"yes"}`)
	if code != 400 {
		t.Fatal("invalid boolean accepted")
	}
}
