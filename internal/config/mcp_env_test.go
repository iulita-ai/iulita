package config

import (
	"reflect"
	"testing"
)

func TestMCPDeploymentEnvironment(t *testing.T) {
	t.Setenv("IULITA_SKILLS_MCP_ENABLED", "true")
	t.Setenv("IULITA_SKILLS_MCP_ALLOWED_ORIGINS", `["https://one.example","https://two.example"]`)
	t.Setenv("IULITA_SKILLS_MCP_OAUTH_REDIRECT", "https://assistant.example/api/mcp/oauth/callback")
	cfg, k, _, err := Load("", testPaths(t))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Skills.MCP.Enabled || !reflect.DeepEqual(cfg.Skills.MCP.AllowedOrigins, []string{"https://one.example", "https://two.example"}) || cfg.Skills.MCP.OAuthRedirect != "https://assistant.example/api/mcp/oauth/callback" {
		t.Fatalf("deployment configuration lost: %+v", cfg.Skills.MCP)
	}
	if k.Exists("skills.mcp.allowed.origins") || k.Exists("skills.mcp.oauth.redirect") {
		t.Fatal("MCP environment leaves were split")
	}
}

func TestMCPRejectsMalformedOriginEnvironment(t *testing.T) {
	for _, value := range []string{"null", "https://one.example,https://two.example", `{"origin":"https://one.example"}`, `[1]`} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("IULITA_SKILLS_MCP_ALLOWED_ORIGINS", value)
			if _, _, _, err := Load("", testPaths(t)); err == nil {
				t.Fatal("malformed deployment allowlist accepted")
			}
		})
	}
}
