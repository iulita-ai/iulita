package skillmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

// validSlugRe matches safe skill slugs: alphanumeric, hyphens, underscores, 1-64 chars.
var validSlugRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// RuntimeCaps describes which built-in skills are active.
// Used to generate fallback instructions for text-only external skills.
type RuntimeCaps struct {
	ShellExecEnabled  bool         // true if shell_exec skill is enabled and configured
	WebfetchAvailable bool         // true if webfetch skill is registered and enabled
	HTTPClient        *http.Client // SSRF-safe HTTP client for proxy skills (optional)
}

// Manager orchestrates external skill installation, loading, and lifecycle.
type Manager struct {
	store     SkillStore
	registry  *skill.Registry
	sources   map[string]Source
	executors map[string]Executor
	cfg       config.ExternalSkillsConfig
	caps      RuntimeCaps
	log       *zap.Logger
}

// NewManager creates a new external skill manager.
func NewManager(store SkillStore, registry *skill.Registry, cfg config.ExternalSkillsConfig, caps RuntimeCaps, log *zap.Logger) *Manager {
	return &Manager{
		store:     store,
		registry:  registry,
		sources:   make(map[string]Source),
		executors: make(map[string]Executor),
		cfg:       cfg,
		caps:      caps,
		log:       log,
	}
}

// SetAllowShell updates the allow_shell flag at runtime.
func (m *Manager) SetAllowShell(v bool) { m.cfg.AllowShell = v }

// SetAllowDocker updates the allow_docker flag at runtime.
func (m *Manager) SetAllowDocker(v bool) { m.cfg.AllowDocker = v }

// SetAllowWASM updates the allow_wasm flag at runtime.
func (m *Manager) SetAllowWASM(v bool) { m.cfg.AllowWASM = v }

// RegisterSource adds a skill source (e.g. ClawhHub, URL).
func (m *Manager) RegisterSource(s Source) {
	m.sources[s.Name()] = s
}

// RegisterExecutor adds an executor for a given isolation level.
func (m *Manager) RegisterExecutor(e Executor) {
	m.executors[e.IsolationLevel()] = e
}

// LoadAll loads all installed skills from the database into the registry.
// Called at startup.
func (m *Manager) LoadAll(ctx context.Context) error {
	if !m.cfg.Enabled {
		m.log.Info("external skills disabled")
		return nil
	}

	skills, err := m.store.ListInstalledSkills(ctx)
	if err != nil {
		return fmt.Errorf("list installed skills: %w", err)
	}

	loaded := 0
	for _, sk := range skills {
		if err := m.loadSkill(ctx, &sk); err != nil {
			m.log.Warn("failed to load external skill",
				zap.String("slug", sk.Slug),
				zap.Error(err))
			continue
		}
		loaded++
	}

	m.log.Info("loaded external skills", zap.Int("count", loaded), zap.Int("total", len(skills)))
	return nil
}

// Install downloads, verifies, extracts, and registers an external skill.
func (m *Manager) Install(ctx context.Context, sourceType, ref string) (*domain.InstalledSkill, []string, error) {
	if !m.cfg.Enabled {
		return nil, nil, fmt.Errorf("external skills are disabled")
	}

	// Check install limit.
	if m.cfg.MaxInstalled > 0 {
		existing, err := m.store.ListInstalledSkills(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("check install limit: %w", err)
		}
		if len(existing) >= m.cfg.MaxInstalled {
			return nil, nil, fmt.Errorf("max installed skills reached (%d)", m.cfg.MaxInstalled)
		}
	}

	src, ok := m.sources[sourceType]
	if !ok {
		return nil, nil, fmt.Errorf("unknown source: %q", sourceType)
	}

	// Resolve skill metadata.
	skillRef, err := src.Resolve(ctx, ref)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %q: %w", ref, err)
	}

	// Validate slug to prevent path traversal.
	if !validSlugRe.MatchString(skillRef.Slug) {
		return nil, nil, fmt.Errorf("invalid skill slug %q: must be alphanumeric with hyphens/underscores", skillRef.Slug)
	}

	// Create temp directory for download.
	tmpDir, err := os.MkdirTemp(m.cfg.Dir, "install-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Download archive (or get directory path for local source).
	archivePath, checksum, err := src.Download(ctx, skillRef, tmpDir)
	if err != nil {
		return nil, nil, fmt.Errorf("download: %w", err)
	}

	var extractDir string
	if sourceType == "local" {
		// Local source: archivePath is the directory itself, no extraction needed.
		extractDir = archivePath
	} else {
		// Verify checksum if provided.
		if skillRef.Checksum != "" {
			if _, err := VerifyChecksum(archivePath, skillRef.Checksum); err != nil {
				return nil, nil, err
			}
		} else if checksum == "" {
			checksum, _ = VerifyChecksum(archivePath, "")
		}

		// Extract to temp extraction directory.
		extractDir = filepath.Join(tmpDir, "extracted")
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			return nil, nil, fmt.Errorf("create extract dir: %w", err)
		}
		if err := ExtractZIP(archivePath, extractDir); err != nil {
			return nil, nil, fmt.Errorf("extract: %w", err)
		}
	}

	// Parse manifest.
	parsed, err := ParseExternalManifest(extractDir, skillRef.Slug, sourceType, skillRef.SourceRef)
	if err != nil {
		return nil, nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Validate isolation against config.
	if err := m.validateIsolation(parsed.Manifest.External.Isolation); err != nil {
		return nil, nil, err
	}

	// Atomic move to final directory.
	finalDir := filepath.Join(m.cfg.Dir, skillRef.Slug)
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, nil, fmt.Errorf("remove existing: %w", err)
	}
	if err := os.Rename(extractDir, finalDir); err != nil {
		return nil, nil, fmt.Errorf("move to final dir: %w", err)
	}

	// Update install dir in manifest.
	parsed.Manifest.External.InstallDir = finalDir

	// Write origin metadata.
	if err := writeOrigin(finalDir, skillRef); err != nil {
		m.log.Warn("failed to write origin file", zap.Error(err))
	}

	// Persist to database.
	now := time.Now()
	installed := &domain.InstalledSkill{
		Slug:            skillRef.Slug,
		Name:            parsed.Manifest.Name,
		Version:         skillRef.Version,
		Source:          sourceType,
		SourceRef:       skillRef.SourceRef,
		Isolation:       domain.IsolationLevel(parsed.Manifest.External.Isolation),
		InstallDir:      finalDir,
		Enabled:         true,
		Checksum:        checksum,
		Description:     parsed.Manifest.Description,
		Author:          skillRef.Author,
		Tags:            joinTags(skillRef.Tags),
		Capabilities:    jsonArray(parsed.Manifest.Capabilities),
		ConfigKeys:      jsonArray(parsed.Manifest.ConfigKeys),
		SecretKeys:      jsonArray(parsed.Manifest.SecretKeys),
		RequiresBins:    jsonArray(parsed.Requires.Bins),
		RequiresEnv:     jsonArray(parsed.Requires.Env),
		AllowedTools:    jsonArray(parsed.AllowedTools),
		HasCode:         parsed.HasCode,
		InstallWarnings: jsonArray(parsed.Warnings),
		InstalledAt:     now,
	}

	if err := m.store.SaveInstalledSkill(ctx, installed); err != nil {
		// Rollback: remove installed files on DB failure.
		if rmErr := os.RemoveAll(finalDir); rmErr != nil {
			m.log.Warn("failed to rollback install directory", zap.String("dir", finalDir), zap.Error(rmErr))
		}
		return nil, nil, fmt.Errorf("save: %w", err)
	}

	// Load into registry.
	if err := m.loadSkill(ctx, installed); err != nil {
		return nil, parsed.Warnings, fmt.Errorf("load after install: %w", err)
	}

	// Warn if skill requires shell bins that aren't available.
	if parsed.Manifest.External != nil &&
		len(parsed.Requires.Bins) > 0 {

		iso := parsed.Manifest.External.Isolation
		needsWarn := (iso == "text_only" && !m.caps.ShellExecEnabled) ||
			(iso == "shell" && m.executors["shell"] == nil)

		if needsWarn {
			binList := strings.Join(parsed.Requires.Bins, ", ")
			w := fmt.Sprintf(
				"This skill requires shell commands (%s) but shell_exec is disabled.",
				binList,
			)
			if m.caps.WebfetchAvailable {
				w += " webfetch has been injected as a fallback — the LLM will use webfetch instead of curl/wget for HTTP requests."
			} else {
				w += " The skill may not work correctly without shell access."
			}
			parsed.Warnings = append(parsed.Warnings, w)
		}
	}

	return installed, parsed.Warnings, nil
}

// Uninstall removes an installed skill from the registry and disk.
func (m *Manager) Uninstall(ctx context.Context, slug string) error {
	sk, err := m.store.GetInstalledSkill(ctx, slug)
	if err != nil {
		return fmt.Errorf("get skill %q: %w", slug, err)
	}

	// Remove from registry.
	m.registry.UnregisterSkill(slug)

	// Remove from disk.
	if sk.InstallDir != "" {
		if err := os.RemoveAll(sk.InstallDir); err != nil {
			m.log.Warn("failed to remove skill directory", zap.String("dir", sk.InstallDir), zap.Error(err))
		}
	}

	// Remove from database.
	return m.store.DeleteInstalledSkill(ctx, slug)
}

// Enable enables an installed skill.
func (m *Manager) Enable(ctx context.Context, slug string) error {
	sk, err := m.store.GetInstalledSkill(ctx, slug)
	if err != nil {
		return err
	}
	sk.Enabled = true
	now := time.Now()
	sk.UpdatedAt = &now
	if err := m.store.UpdateInstalledSkill(ctx, sk); err != nil {
		return err
	}
	m.registry.EnableSkill(slug)
	return nil
}

// Disable disables an installed skill.
func (m *Manager) Disable(ctx context.Context, slug string) error {
	sk, err := m.store.GetInstalledSkill(ctx, slug)
	if err != nil {
		return err
	}
	sk.Enabled = false
	now := time.Now()
	sk.UpdatedAt = &now
	if err := m.store.UpdateInstalledSkill(ctx, sk); err != nil {
		return err
	}
	m.registry.DisableSkill(slug)
	return nil
}

// ListInstalled returns all installed external skills.
func (m *Manager) ListInstalled(ctx context.Context) ([]domain.InstalledSkill, error) {
	return m.store.ListInstalledSkills(ctx)
}

// GetInstalled returns a single installed skill by slug.
func (m *Manager) GetInstalled(ctx context.Context, slug string) (*domain.InstalledSkill, error) {
	return m.store.GetInstalledSkill(ctx, slug)
}

// ResolveMarketplace fetches skill metadata from a source without downloading.
func (m *Manager) ResolveMarketplace(ctx context.Context, sourceType, ref string) (*SkillRef, error) {
	src, ok := m.sources[sourceType]
	if !ok {
		return nil, fmt.Errorf("unknown source: %q", sourceType)
	}
	return src.Resolve(ctx, ref)
}

// Search searches a specific source for skills.
func (m *Manager) Search(ctx context.Context, sourceType, query string, limit int) ([]SkillRef, error) {
	src, ok := m.sources[sourceType]
	if !ok {
		return nil, fmt.Errorf("unknown source: %q", sourceType)
	}
	return src.Search(ctx, query, limit)
}

// loadSkill loads a single installed skill into the registry.
func (m *Manager) loadSkill(ctx context.Context, sk *domain.InstalledSkill) error {
	// Check that install directory exists.
	if _, err := os.Stat(sk.InstallDir); err != nil {
		return fmt.Errorf("install dir missing: %w", err)
	}

	parsed, err := ParseExternalManifest(sk.InstallDir, sk.Slug, sk.Source, sk.SourceRef)
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	isolation := string(sk.Isolation)

	// Enforce isolation policy — prevents loading skills that were installed
	// under a different policy (e.g. AllowShell was true, now false).
	if err := m.validateIsolation(isolation); err != nil {
		m.log.Warn("skipping skill due to isolation policy",
			zap.String("slug", sk.Slug),
			zap.String("isolation", isolation),
			zap.Error(err),
		)
		return nil
	}

	effectiveMode := isolation // track actual runtime mode

	switch isolation {
	case "text_only":
		if len(parsed.Requires.Bins) > 0 && !m.caps.ShellExecEnabled {
			effectiveMode = m.registerWebfetchProxyOrTextOnly(sk.Slug, parsed)
		} else {
			// Pure text-only skill (no bins required) or shell_exec is enabled.
			m.registry.RegisterExternalWithManifest(newTextOnlySkill(parsed.Manifest), parsed.Manifest)
		}

	case "shell":
		exec, ok := m.executors[isolation]
		if ok && exec.Available() {
			m.registry.RegisterExternalWithManifest(newExecutableSkill(parsed.Manifest, exec, parsed.Entrypoint), parsed.Manifest)
		} else {
			effectiveMode = m.registerWebfetchProxyOrTextOnly(sk.Slug, parsed)
		}

	case "docker", "wasm":
		exec, ok := m.executors[isolation]
		if !ok {
			return fmt.Errorf("no executor for isolation %q", isolation)
		}
		if !exec.Available() {
			m.log.Warn("executor not available, registering as disabled",
				zap.String("slug", sk.Slug),
				zap.String("isolation", isolation))
			m.registry.RegisterExternalWithManifest(newTextOnlySkill(parsed.Manifest), parsed.Manifest)
			m.registry.DisableSkill(sk.Slug)
			effectiveMode = "text_only"
		} else {
			m.registry.RegisterExternalWithManifest(newExecutableSkill(parsed.Manifest, exec, parsed.Entrypoint), parsed.Manifest)
		}

	default:
		return fmt.Errorf("unknown isolation level: %q", isolation)
	}

	// Persist effective mode if it differs from declared isolation.
	if effectiveMode != isolation && sk.EffectiveMode != effectiveMode {
		sk.EffectiveMode = effectiveMode
		if err := m.store.UpdateInstalledSkill(ctx, sk); err != nil {
			m.log.Warn("failed to persist effective mode",
				zap.String("slug", sk.Slug),
				zap.String("effective_mode", effectiveMode),
				zap.Error(err),
			)
		}
	}

	if !sk.Enabled {
		m.registry.DisableSkill(sk.Slug)
	}

	return nil
}

// validateIsolation checks if the requested isolation level is allowed by config.
func (m *Manager) validateIsolation(isolation string) error {
	switch isolation {
	case "text_only":
		return nil
	case "shell":
		if !m.cfg.AllowShell {
			return fmt.Errorf("shell-isolation skills are disabled (set skills.external.allow_shell=true)")
		}
	case "docker":
		if !m.cfg.AllowDocker {
			return fmt.Errorf("docker-isolation skills are disabled (set skills.external.allow_docker=true)")
		}
	case "wasm":
		if !m.cfg.AllowWASM {
			return fmt.Errorf("wasm-isolation skills are disabled (set skills.external.allow_wasm=true)")
		}
	default:
		return fmt.Errorf("unknown isolation level: %q", isolation)
	}
	return nil
}

// registerWebfetchProxyOrTextOnly registers a skill that requires shell bins
// as either a webfetch proxy (if webfetch is available) or plain text-only.
// Returns the effective runtime mode ("webfetch_proxy" or "text_only").
func (m *Manager) registerWebfetchProxyOrTextOnly(slug string, parsed *ParsedManifest) string {
	if len(parsed.Requires.Bins) > 0 && m.caps.WebfetchAvailable && m.caps.HTTPClient != nil {
		m.log.Info("registering skill as webfetch proxy",
			zap.String("slug", slug),
			zap.Strings("bins", parsed.Requires.Bins),
		)
		// Create proxy BEFORE clearing SystemPrompt — it parses URLs from the text.
		parsed.Manifest.ForceTriggers = buildForceTriggers(parsed.Manifest.Name)
		proxy := newWebfetchProxySkill(parsed.Manifest, m.caps.HTTPClient, parsed.Requires.Bins)
		if len(proxy.urlHints) == 0 {
			// No URL patterns found — proxy would be useless, fall back to text-only.
			m.log.Warn("no URL patterns found in skill instructions, registering as text-only instead of proxy",
				zap.String("slug", slug),
				zap.Strings("bins", parsed.Requires.Bins),
			)
			m.registry.RegisterExternalWithManifest(newTextOnlySkill(parsed.Manifest), parsed.Manifest)
			return "text_only"
		}
		m.log.Info("proxy skill URL hints", zap.Strings("urls", proxy.urlHints))
		// Now clear the system prompt so the LLM doesn't hallucinate from it.
		parsed.Manifest.SystemPrompt = ""
		m.registry.RegisterExternalWithManifest(proxy, parsed.Manifest)
		return "webfetch_proxy"
	}
	m.log.Warn("skill requires shell bins but no executor or webfetch available, registering as text-only",
		zap.String("slug", slug),
		zap.Strings("bins", parsed.Requires.Bins),
	)
	m.registry.RegisterExternalWithManifest(newTextOnlySkill(parsed.Manifest), parsed.Manifest)
	return "text_only"
}

func writeOrigin(dir string, ref *SkillRef) error {
	origin := map[string]any{
		"slug":         ref.Slug,
		"source":       ref.Source,
		"source_ref":   ref.SourceRef,
		"version":      ref.Version,
		"checksum":     ref.Checksum,
		"installed_at": time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(origin, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".skill-origin.json"), data, 0644)
}

// skillTriggerTranslations maps English skill-related keywords to common translations.
// Used to auto-detect when a user's message should force a specific tool.
var skillTriggerTranslations = map[string][]string{
	"weather":   {"погод", "weather", "forecast", "прогноз", "wttr", "метео"},
	"translate": {"перевод", "перевед", "translate", "translation"},
	"currency":  {"курс валют", "exchange rate", "конверт валют"},
	"news":      {"новост", "news", "headlines"},
	"search":    {"найди", "поиск", "search", "find"},
}

// buildForceTriggers returns lowercase trigger keywords for a skill name.
func buildForceTriggers(skillName string) []string {
	lower := strings.ToLower(skillName)
	if triggers, ok := skillTriggerTranslations[lower]; ok {
		return triggers
	}
	// Default: use the skill name itself as trigger.
	return []string{lower}
}

// jsonArray marshals a string slice to a JSON array string for DB storage.
// Returns "" for nil/empty slices.
func jsonArray(items []string) string {
	if len(items) == 0 {
		return ""
	}
	data, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(data)
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	result := ""
	for i, t := range tags {
		if i > 0 {
			result += ","
		}
		result += t
	}
	return result
}
