package dashboard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
)

// handleWizardStatus returns the current wizard state.
func (s *Server) handleWizardStatus(c *fiber.Ctx) error {
	wizardCompleted := false
	if s.configStore != nil {
		if val, ok := s.configStore.Get("_system.wizard_completed"); ok && val == "true" {
			wizardCompleted = true
		}
	}

	hasLLM := false
	if s.configStore != nil {
		if v, ok := s.configStore.GetEffective("claude.api_key"); ok && v != "" {
			hasLLM = true
		}
		if v, ok := s.configStore.GetEffective("openai.api_key"); ok && v != "" {
			if m, ok2 := s.configStore.GetEffective("openai.model"); ok2 && m != "" {
				hasLLM = true
			}
		}
		if v, ok := s.configStore.GetEffective("ollama.url"); ok && v != "" {
			if m, ok2 := s.configStore.GetEffective("ollama.model"); ok2 && m != "" {
				hasLLM = true
			}
		}
	}

	return c.JSON(fiber.Map{
		"wizard_completed":   wizardCompleted,
		"setup_mode":         s.setupMode,
		"encryption_enabled": s.configStore != nil && s.configStore.EncryptionEnabled(),
		"has_llm_provider":   hasLLM,
	})
}

// handleWizardComplete marks the wizard as completed.
// Requires at least one LLM provider to be configured.
func (s *Server) handleWizardComplete(c *fiber.Ctx) error {
	if s.configStore == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "config store not available"})
	}

	// Verify at least one LLM provider is configured.
	hasLLM := false
	if v, ok := s.configStore.GetEffective("claude.api_key"); ok && v != "" {
		hasLLM = true
	}
	if v, ok := s.configStore.GetEffective("openai.api_key"); ok && v != "" {
		if m, ok2 := s.configStore.GetEffective("openai.model"); ok2 && m != "" {
			hasLLM = true
		}
	}
	if v, ok := s.configStore.GetEffective("ollama.url"); ok && v != "" {
		if m, ok2 := s.configStore.GetEffective("ollama.model"); ok2 && m != "" {
			hasLLM = true
		}
	}
	if !hasLLM {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "at least one LLM provider must be configured before completing setup",
		})
	}

	claims := auth.GetClaims(c)
	updatedBy := "wizard"
	if claims != nil {
		updatedBy = claims.Username
	}

	if err := s.configStore.Set(c.Context(), "_system.wizard_completed", "true", updatedBy, false); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Create sentinel file so TOML is skipped on next startup (DB is now the source of truth).
	paths := config.ResolvePaths()
	sentinel := filepath.Join(paths.ConfigDir, "db_managed")
	if err := config.WriteSentinelFile(sentinel); err != nil {
		s.logger.Warn("failed to write db_managed sentinel", zap.Error(err))
	}
	_ = s.configStore.Set(c.Context(), "_system.toml_ignored", "true", updatedBy, false)

	return c.JSON(fiber.Map{
		"status":  "completed",
		"message": "Setup complete. Restart the application to activate all services.",
	})
}

// handleImportTOML imports all values from the base config (TOML + env) into DB overrides.
// After import, creates a sentinel file so TOML is skipped on next startup.
func (s *Server) handleImportTOML(c *fiber.Ctx) error {
	if s.configStore == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "config store not available"})
	}

	claims := auth.GetClaims(c)
	updatedBy := "toml-import"
	if claims != nil {
		updatedBy = fmt.Sprintf("toml-import:%s", claims.Username)
	}

	schema := config.CoreConfigSchema()
	imported := 0
	skipped := 0
	var errors []string

	for _, section := range schema {
		for _, field := range section.Fields {
			// Get value from base config (TOML + env).
			val, ok := s.configStore.GetBaseValue(field.Key)
			if !ok || val == "" {
				continue
			}

			// Skip if already has a DB override.
			if s.configStore.HasOverride(field.Key) {
				skipped++
				continue
			}

			// Determine if this field should be encrypted.
			encrypt := field.Secret

			if err := s.configStore.SetForImport(c.Context(), field.Key, val, updatedBy, encrypt); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", field.Key, err))
				continue
			}
			imported++
		}
	}

	// Also import known keys that are in the base config but not in the schema
	// (e.g., telegram.token, storage.path — restart-only keys).
	restartKeys := []struct {
		key    string
		secret bool
	}{
		{"telegram.token", true},
		{"storage.path", false},
		{"server.address", false},
		{"proxy.url", false},
	}
	for _, rk := range restartKeys {
		val, ok := s.configStore.GetBaseValue(rk.key)
		if !ok || val == "" {
			continue
		}
		if s.configStore.HasOverride(rk.key) {
			skipped++
			continue
		}
		if err := s.configStore.SetForImport(c.Context(), rk.key, val, updatedBy, rk.secret); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", rk.key, err))
			continue
		}
		imported++
	}

	resp := fiber.Map{
		"imported": imported,
		"skipped":  skipped,
		"status":   "ok",
	}
	if len(errors) > 0 {
		resp["errors"] = errors
		resp["status"] = "partial"
	}

	return c.JSON(resp)
}

// handleWizardSchema returns the config schema with current effective values populated.
// This is used by the wizard UI to show fields with pre-filled values.
func (s *Server) handleWizardSchema(c *fiber.Ctx) error {
	if s.configStore == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "config store not available"})
	}

	sections := config.CoreConfigSchema()

	// Filter to wizard-relevant sections and populate values.
	var wizardSections []fiber.Map
	for _, section := range sections {
		var fields []fiber.Map
		for _, field := range section.Fields {
			val, hasVal := s.configStore.GetEffective(field.Key)
			hasOverride := s.configStore.HasOverride(field.Key)

			// Mask secret values.
			displayVal := val
			if field.Secret && hasVal && val != "" {
				displayVal = "***"
			}

			f := fiber.Map{
				"key":          field.Key,
				"label":        field.Label,
				"description":  field.Description,
				"type":         field.Type,
				"default":      field.Default,
				"secret":       field.Secret,
				"required":     field.Required,
				"section":      field.Section,
				"value":        displayVal,
				"has_value":    hasVal && val != "",
				"has_override": hasOverride,
			}
			if len(field.Options) > 0 {
				f["options"] = field.Options
			}
			if field.ModelSource != "" {
				f["model_source"] = string(field.ModelSource)
			}
			fields = append(fields, f)
		}

		sectionProviders := []string{"claude", "openai", "ollama"}
		isLLMSection := false
		for _, p := range sectionProviders {
			if strings.EqualFold(section.Name, p) {
				isLLMSection = true
				break
			}
		}

		wizardSections = append(wizardSections, fiber.Map{
			"name":        section.Name,
			"label":       section.Label,
			"description": section.Description,
			"fields":      fields,
			"optional":    section.Optional,
			"is_llm":      isLLMSection,
		})
	}

	return c.JSON(fiber.Map{
		"sections":           wizardSections,
		"encryption_enabled": s.configStore.EncryptionEnabled(),
	})
}
