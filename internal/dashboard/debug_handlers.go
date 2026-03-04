package dashboard

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/iulita-ai/iulita/internal/config"
)

// handleConfigDebug returns a diagnostic view of all config layers and paths.
// Admin-only endpoint for troubleshooting config loading issues.
func (s *Server) handleConfigDebug(c *fiber.Ctx) error {
	if s.configStore == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "config store not available"})
	}

	paths := config.ResolvePaths()
	sections := config.CoreConfigSchema()

	// Collect all keys from schema + restart-only + known dynamic keys.
	var rows []fiber.Map
	for _, section := range sections {
		for _, field := range section.Fields {
			rows = append(rows, s.buildDebugRow(field.Key, field.Secret, field.Label, section.Name))
		}
	}

	// Add restart-only keys not in schema.
	restartKeys := []struct {
		key     string
		secret  bool
		label   string
		section string
	}{
		{"telegram.token", true, "Telegram Token", "telegram"},
		{"storage.path", false, "Storage Path", "storage"},
		{"server.address", false, "Server Address", "server"},
		{"server.enabled", false, "Server Enabled", "server"},
		{"proxy.url", false, "Proxy URL", "proxy"},
		{"auth.jwt_secret", true, "JWT Secret", "auth"},
		{"auth.token_expiry", false, "Token Expiry", "auth"},
		{"auth.refresh_expiry", false, "Refresh Expiry", "auth"},
		{"auth.allow_register", false, "Allow Register", "auth"},
		{"auth.multi_user", false, "Multi-user", "auth"},
		{"log.level", false, "Log Level", "log"},
		{"log.encoding", false, "Log Encoding", "log"},
	}
	seen := make(map[string]bool)
	for _, r := range rows {
		seen[r["key"].(string)] = true
	}
	for _, rk := range restartKeys {
		if !seen[rk.key] {
			rows = append(rows, s.buildDebugRow(rk.key, rk.secret, rk.label, rk.section))
		}
	}

	// Resolved paths.
	configFile := paths.ConfigFile()
	configExists := fileExists(configFile)
	dbFile := s.configStore.Base().Storage.Path
	sentinelFile := paths.ConfigDir + "/db_managed"
	sentinelExists := fileExists(sentinelFile)

	// Environment variables (IULITA_ prefix).
	var envVars []fiber.Map
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "IULITA_") {
			parts := strings.SplitN(env, "=", 2)
			val := ""
			if len(parts) > 1 {
				val = parts[1]
			}
			// Mask secrets.
			key := parts[0]
			if strings.Contains(strings.ToLower(key), "key") ||
				strings.Contains(strings.ToLower(key), "secret") ||
				strings.Contains(strings.ToLower(key), "token") {
				if len(val) > 4 {
					val = val[:4] + "***"
				}
			}
			envVars = append(envVars, fiber.Map{"name": key, "value": val})
		}
	}

	return c.JSON(fiber.Map{
		"rows": rows,
		"paths": fiber.Map{
			"config_dir":      paths.ConfigDir,
			"data_dir":        paths.DataDir,
			"cache_dir":       paths.CacheDir,
			"state_dir":       paths.StateDir,
			"config_file":     configFile,
			"config_exists":   configExists,
			"database_file":   dbFile,
			"models_dir":      paths.ModelsDir(),
			"log_file":        paths.LogFile(),
			"sentinel_file":   sentinelFile,
			"sentinel_exists": sentinelExists,
			"encryption_key":  paths.EncryptionKeyFile(),
		},
		"env_vars":           envVars,
		"encryption_enabled": s.configStore.EncryptionEnabled(),
	})
}

func (s *Server) buildDebugRow(key string, secret bool, label, section string) fiber.Map {
	baseVal, hasBase := s.configStore.GetBaseValue(key)
	dbVal, hasDB := s.configStore.Get(key)
	effectiveVal, _ := s.configStore.GetEffective(key)

	// Mask secrets.
	if secret {
		if hasBase && baseVal != "" {
			baseVal = maskValue(baseVal)
		}
		if hasDB && dbVal != "" {
			dbVal = maskValue(dbVal)
		}
		if effectiveVal != "" {
			effectiveVal = maskValue(effectiveVal)
		}
	}

	source := "default"
	if hasDB {
		source = "database"
	} else if hasBase {
		source = "config"
	}

	return fiber.Map{
		"key":       key,
		"label":     label,
		"section":   section,
		"secret":    secret,
		"base":      baseVal,
		"has_base":  hasBase,
		"db":        dbVal,
		"has_db":    hasDB,
		"effective": effectiveVal,
		"source":    source,
	}
}

func maskValue(v string) string {
	if len(v) <= 4 {
		return "***"
	}
	return v[:4] + "***"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
