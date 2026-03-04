package skillinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skillmgr"
)

// ConfigStore abstracts the config store for skill management operations.
type ConfigStore interface {
	GetEffective(key string) (string, bool)
	HasOverride(key string) bool
	IsSecretKey(key string) bool
	Set(ctx context.Context, key, value, updatedBy string, encrypt bool) error
	Delete(ctx context.Context, key string) error
}

// ExternalSkillManager abstracts skillmgr.Manager for external skill operations.
// Defined as interface for testability; skillmgr.Manager satisfies it directly.
type ExternalSkillManager interface {
	Search(ctx context.Context, sourceType, query string, limit int) ([]skillmgr.SkillRef, error)
	Install(ctx context.Context, sourceType, ref string) (*domain.InstalledSkill, []string, error)
	Uninstall(ctx context.Context, slug string) error
	ListInstalled(ctx context.Context) ([]domain.InstalledSkill, error)
	GetInstalled(ctx context.Context, slug string) (*domain.InstalledSkill, error)
}

// UserStore abstracts user lookup for username resolution.
type UserStore interface {
	GetUsername(ctx context.Context, userID string) string
}

// simpleUserStore is a minimal UserStore that returns the userID as-is.
type simpleUserStore struct{}

func (simpleUserStore) GetUsername(_ context.Context, userID string) string { return userID }

// Skill manages skills: list, enable/disable, view and update configuration.
type Skill struct {
	registry *skill.Registry
	config   ConfigStore // nil = read-only mode (list only)
	users    UserStore
	extMgr   ExternalSkillManager // nil = external skills disabled
}

// New creates a skill management skill.
// config may be nil (disables config read/write operations).
func New(registry *skill.Registry, config ConfigStore) *Skill {
	return &Skill{
		registry: registry,
		config:   config,
		users:    simpleUserStore{},
	}
}

// NewWithExternalManager creates a skill management skill with external skill support.
func NewWithExternalManager(registry *skill.Registry, config ConfigStore, extMgr ExternalSkillManager) *Skill {
	return &Skill{
		registry: registry,
		config:   config,
		users:    simpleUserStore{},
		extMgr:   extMgr,
	}
}

func (s *Skill) Name() string { return "skills" }

func (s *Skill) Description() string {
	desc := "Manage skills and system configuration. " +
		"Use action='list' (default) to see all skills. " +
		"Use action='list_config' to see all config sections (claude, openai, ollama, routing, etc). " +
		"Admin users can use 'enable', 'disable', 'get_config', 'set_config' actions. " +
		"For skill config: skill_name='memory'. For system config: skill_name='claude', 'openai', 'ollama', 'routing', etc."
	if s.extMgr != nil {
		desc += " External skills: 'search_external' (search marketplace), 'install_external', 'uninstall_external', 'list_external', 'update_external'."
	}
	return desc
}

func (s *Skill) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {
			"type": "string",
			"enum": ["list", "list_config", "enable", "disable", "get_config", "set_config", "search_external", "install_external", "uninstall_external", "list_external", "update_external"],
			"description": "Action to perform. Default is 'list'. 'list_config' shows system config sections. 'get_config'/'set_config' work for both skill groups and system sections. External skill actions: 'search_external' (search marketplace), 'install_external', 'uninstall_external', 'list_external', 'update_external'."
		},
		"skill_name": {
			"type": "string",
			"description": "Skill name, manifest group name, or system config section (claude, openai, ollama, proxy, routing, embedding, cache, cost, app, log, server)."
		},
		"config_key": {
			"type": "string",
			"description": "Config key to update (required for set_config). Full dotted key like 'claude.model' or 'openai.fallback'."
		},
		"config_value": {
			"type": "string",
			"description": "New value for the config key (required for set_config). Use empty string to delete override."
		},
		"source": {
			"type": "string",
			"description": "Source type for external skills: 'clawhub' (marketplace, default), 'url' (direct download), 'local' (filesystem path)."
		},
		"query": {
			"type": "string",
			"description": "Search query for search_external action."
		},
		"ref": {
			"type": "string",
			"description": "Skill reference for install_external: slug for clawhub (e.g. 'weather-brief'), full URL for url source, local path for local source."
		},
		"slug": {
			"type": "string",
			"description": "Skill slug for uninstall_external and update_external actions."
		},
		"limit": {
			"type": "integer",
			"description": "Max results for search_external (default 20, max 50)."
		}
	}
}`)
}

type input struct {
	Action      string `json:"action"`
	SkillName   string `json:"skill_name"`
	ConfigKey   string `json:"config_key"`
	ConfigValue string `json:"config_value"`
	// External skill fields:
	Source string `json:"source"`
	Query  string `json:"query"`
	Ref    string `json:"ref"`
	Slug   string `json:"slug"`
	Limit  int    `json:"limit"`
}

func (s *Skill) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in input
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in)
	}

	action := in.Action
	if action == "" {
		action = "list"
	}

	switch action {
	case "list":
		return s.actionList()
	case "list_config":
		return s.actionListConfig(ctx)
	case "enable":
		return s.actionEnable(ctx, in.SkillName)
	case "disable":
		return s.actionDisable(ctx, in.SkillName)
	case "get_config":
		return s.actionGetConfig(ctx, in.SkillName)
	case "set_config":
		return s.actionSetConfig(ctx, in.SkillName, in.ConfigKey, in.ConfigValue)
	case "search_external":
		return s.actionSearchExternal(ctx, in.Source, in.Query, in.Limit)
	case "install_external":
		return s.actionInstallExternal(ctx, in.Source, in.Ref)
	case "uninstall_external":
		return s.actionUninstallExternal(ctx, in.Slug)
	case "list_external":
		return s.actionListExternal(ctx)
	case "update_external":
		return s.actionUpdateExternal(ctx, in.Slug)
	default:
		return fmt.Sprintf("Unknown action %q. Use: list, list_config, enable, disable, get_config, set_config.", action), nil
	}
}

// actionList returns all skills (enabled and disabled) with their status.
func (s *Skill) actionList() (string, error) {
	var b strings.Builder

	all := s.registry.AllSkills()
	sort.Slice(all, func(i, j int) bool {
		return all[i].Skill.Name() < all[j].Skill.Name()
	})

	// Group by manifest.
	type groupInfo struct {
		manifestName string
		skills       []skill.SkillStatus
	}
	groups := make(map[string]*groupInfo)
	var standalone []skill.SkillStatus
	var groupOrder []string

	for _, ss := range all {
		if ss.ManifestGroup != "" {
			g, ok := groups[ss.ManifestGroup]
			if !ok {
				g = &groupInfo{manifestName: ss.ManifestGroup}
				groups[ss.ManifestGroup] = g
				groupOrder = append(groupOrder, ss.ManifestGroup)
			}
			g.skills = append(g.skills, ss)
		} else {
			standalone = append(standalone, ss)
		}
	}
	sort.Strings(groupOrder)

	// Print groups.
	if len(groupOrder) > 0 {
		b.WriteString("Skill Groups:\n")
		for _, gName := range groupOrder {
			g := groups[gName]
			hasConfig := s.manifestHasConfig(gName)
			fmt.Fprintf(&b, "\n[%s]", gName)
			if hasConfig {
				b.WriteString(" (configurable)")
			}
			b.WriteString("\n")
			for _, ss := range g.skills {
				status := s.formatStatus(ss)
				fmt.Fprintf(&b, "  - %s: %s %s\n", ss.Skill.Name(), ss.Skill.Description(), status)
			}
		}
	}

	// Print standalone.
	if len(standalone) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Standalone Skills:\n")
		for _, ss := range standalone {
			status := s.formatStatus(ss)
			fmt.Fprintf(&b, "- %s: %s %s\n", ss.Skill.Name(), ss.Skill.Description(), status)
		}
	}

	// Text-only skills.
	var textSkills []*skill.Manifest
	for _, m := range s.registry.Manifests() {
		if m.Type == skill.TextOnly {
			textSkills = append(textSkills, m)
		}
	}
	if len(textSkills) > 0 {
		sort.Slice(textSkills, func(i, j int) bool { return textSkills[i].Name < textSkills[j].Name })
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Text Skills (system prompt):\n")
		for _, m := range textSkills {
			desc := m.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&b, "- %s: %s\n", m.Name, desc)
		}
	}

	if b.Len() == 0 {
		return "No skills registered.", nil
	}

	return b.String(), nil
}

func (s *Skill) formatStatus(ss skill.SkillStatus) string {
	if !ss.Enabled {
		return "[DISABLED]"
	}
	if !ss.HasCapabilities {
		return "[NO CREDENTIALS]"
	}
	return "[enabled]"
}

func (s *Skill) manifestHasConfig(manifestName string) bool {
	m, ok := s.registry.GetManifest(manifestName)
	return ok && len(m.ConfigKeys) > 0
}

// requireAdmin checks that the caller has admin role.
func requireAdmin(ctx context.Context) error {
	role := skill.UserRoleFrom(ctx)
	if role != "admin" {
		return fmt.Errorf("this action requires admin privileges")
	}
	return nil
}

// actionEnable enables a skill or skill group.
func (s *Skill) actionEnable(ctx context.Context, name string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if name == "" {
		return "skill_name is required for enable action.", nil
	}

	// Check if it's a manifest group.
	if _, ok := s.registry.GetManifest(name); ok {
		s.registry.EnableGroup(name)
		warn := s.persistDelete(ctx, "skills."+name+".enabled")
		skills := s.registry.GroupSkills(name)
		return fmt.Sprintf("Enabled skill group %q (%s).%s", name, strings.Join(skills, ", "), warn), nil
	}

	// Individual skill.
	if _, ok := s.registry.Get(name); !ok {
		// Skill might be disabled, try enabling anyway.
		if s.registry.IsDisabled(name) {
			s.registry.EnableSkill(name)
			warn := s.persistDelete(ctx, "skills."+name+".enabled")
			return fmt.Sprintf("Enabled skill %q.%s", name, warn), nil
		}
		return fmt.Sprintf("Skill %q not found.", name), nil
	}

	s.registry.EnableSkill(name)
	warn := s.persistDelete(ctx, "skills."+name+".enabled")
	return fmt.Sprintf("Enabled skill %q.%s", name, warn), nil
}

// actionDisable disables a skill or skill group.
func (s *Skill) actionDisable(ctx context.Context, name string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if name == "" {
		return "skill_name is required for disable action.", nil
	}
	if name == s.Name() {
		return "Cannot disable the skills management skill.", nil
	}

	// Check if it's a manifest group.
	if _, ok := s.registry.GetManifest(name); ok {
		s.registry.DisableGroup(name)
		warn := s.persistSet(ctx, "skills."+name+".enabled", "false")
		skills := s.registry.GroupSkills(name)
		return fmt.Sprintf("Disabled skill group %q (%s).%s", name, strings.Join(skills, ", "), warn), nil
	}

	// Individual skill.
	allSkills := s.registry.AllSkills()
	found := false
	for _, ss := range allSkills {
		if ss.Skill.Name() == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Sprintf("Skill %q not found.", name), nil
	}

	s.registry.DisableSkill(name)
	warn := s.persistSet(ctx, "skills."+name+".enabled", "false")
	return fmt.Sprintf("Disabled skill %q.%s", name, warn), nil
}

// actionGetConfig returns the config schema and values for a skill/group or core config section.
func (s *Skill) actionGetConfig(ctx context.Context, name string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if name == "" {
		return "skill_name is required for get_config action.", nil
	}
	if s.config == nil {
		return "Config store not available.", nil
	}

	// Check if it's a core config section first.
	if isCoreConfigSection(name) {
		return s.actionGetCoreConfig(ctx, name)
	}

	manifestName := s.resolveManifestName(name)
	if manifestName == "" {
		return fmt.Sprintf("No configuration found for %q. Use list_config to see available sections.", name), nil
	}

	fields, ok := s.registry.GetSkillConfigSchema(manifestName, s.config)
	if !ok {
		return fmt.Sprintf("No configuration found for %q.", name), nil
	}

	if len(fields) == 0 {
		return fmt.Sprintf("Skill group %q has no configurable keys.", manifestName), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Configuration for %q:\n\n", manifestName)
	for _, f := range fields {
		if f.Secret {
			if f.HasValue {
				fmt.Fprintf(&b, "- %s: *** [set]", f.Key)
			} else {
				fmt.Fprintf(&b, "- %s: [not set]", f.Key)
			}
		} else {
			if f.HasValue {
				fmt.Fprintf(&b, "- %s: %s", f.Key, f.Value)
			} else {
				fmt.Fprintf(&b, "- %s: [not set]", f.Key)
			}
		}
		if f.HasOverride {
			b.WriteString(" (override)")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// actionSetConfig updates a config key for a skill or core config section.
func (s *Skill) actionSetConfig(ctx context.Context, name, key, value string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if name == "" {
		return "skill_name is required for set_config action.", nil
	}
	if key == "" {
		return "config_key is required for set_config action.", nil
	}
	if s.config == nil {
		return "Config store not available.", nil
	}

	// Check if it's a core config section first.
	if isCoreConfigSection(name) {
		return s.actionSetCoreConfig(ctx, name, key, value)
	}

	manifestName := s.resolveManifestName(name)
	if manifestName == "" {
		return fmt.Sprintf("No configuration found for %q. Use list_config to see available sections.", name), nil
	}

	m, ok := s.registry.GetManifest(manifestName)
	if !ok {
		return fmt.Sprintf("Manifest %q not found.", manifestName), nil
	}

	// Validate key belongs to this manifest.
	validKey := false
	for _, ck := range m.ConfigKeys {
		if ck == key {
			validKey = true
			break
		}
	}
	if !validKey {
		return fmt.Sprintf("Key %q is not a valid config key for %q. Valid keys: %s", key, manifestName, strings.Join(m.ConfigKeys, ", ")), nil
	}

	// Delete override if value is empty.
	if value == "" {
		if err := s.config.Delete(ctx, key); err != nil {
			return fmt.Sprintf("Failed to delete config key %q: %v", key, err), nil
		}
		return fmt.Sprintf("Removed override for %q (reverted to base config).", key), nil
	}

	updatedBy := s.resolveUsername(ctx)
	isSecret := s.config.IsSecretKey(key)
	if err := s.config.Set(ctx, key, value, updatedBy, isSecret); err != nil {
		return fmt.Sprintf("Failed to set %q: %v", key, err), nil
	}

	if isSecret {
		return fmt.Sprintf("Updated %q (secret value not displayed).", key), nil
	}
	return fmt.Sprintf("Updated %q = %q.", key, value), nil
}

// resolveManifestName resolves a skill or group name to a manifest name.
func (s *Skill) resolveManifestName(name string) string {
	// Direct manifest lookup.
	if _, ok := s.registry.GetManifest(name); ok {
		return name
	}
	// Check if name is a skill that belongs to a manifest.
	if mName := s.registry.SkillManifest(name); mName != "" {
		return mName
	}
	return ""
}

// resolveUsername extracts a username for audit logging.
func (s *Skill) resolveUsername(ctx context.Context) string {
	userID := skill.UserIDFrom(ctx)
	if userID == "" {
		return "chat"
	}
	if s.users != nil {
		if name := s.users.GetUsername(ctx, userID); name != "" {
			return name
		}
	}
	return userID
}

// persistDelete removes a config override, returning a warning suffix if it fails.
func (s *Skill) persistDelete(ctx context.Context, key string) string {
	if s.config == nil {
		return ""
	}
	if err := s.config.Delete(ctx, key); err != nil {
		return fmt.Sprintf(" Warning: failed to persist change: %v (will revert on restart).", err)
	}
	return ""
}

// persistSet saves a config override, returning a warning suffix if it fails.
func (s *Skill) persistSet(ctx context.Context, key, value string) string {
	if s.config == nil {
		return ""
	}
	updatedBy := s.resolveUsername(ctx)
	if err := s.config.Set(ctx, key, value, updatedBy, false); err != nil {
		return fmt.Sprintf(" Warning: failed to persist change: %v (will revert on restart).", err)
	}
	return ""
}

// actionListConfig lists all core config sections with their descriptions.
func (s *Skill) actionListConfig(ctx context.Context) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}

	schema := config.CoreConfigSchema()
	var b strings.Builder
	b.WriteString("System Configuration Sections:\n\n")
	for _, sec := range schema {
		fmt.Fprintf(&b, "- %s (%s): %s — %d fields\n", sec.Name, sec.Label, sec.Description, len(sec.Fields))
	}
	b.WriteString("\nUse get_config with skill_name=<section> to view details.")
	return b.String(), nil
}

// isCoreConfigSection checks if a name matches a core config section.
func isCoreConfigSection(name string) bool {
	_, ok := config.GetSection(name)
	return ok
}

// actionGetCoreConfig returns the config schema and values for a core section.
func (s *Skill) actionGetCoreConfig(ctx context.Context, sectionName string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.config == nil {
		return "Config store not available.", nil
	}

	section, ok := config.GetSection(sectionName)
	if !ok {
		return fmt.Sprintf("Unknown config section %q.", sectionName), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Configuration: %s (%s)\n%s\n\n", section.Label, section.Name, section.Description)
	for _, f := range section.Fields {
		val, hasVal := s.config.GetEffective(f.Key)
		hasOverride := s.config.HasOverride(f.Key)
		if f.Secret {
			if hasVal && val != "" {
				fmt.Fprintf(&b, "- %s: *** [set]", f.Key)
			} else {
				fmt.Fprintf(&b, "- %s: [not set]", f.Key)
			}
		} else {
			if hasVal && val != "" {
				fmt.Fprintf(&b, "- %s: %s", f.Key, val)
			} else if f.Default != "" {
				fmt.Fprintf(&b, "- %s: %s (default)", f.Key, f.Default)
			} else {
				fmt.Fprintf(&b, "- %s: [not set]", f.Key)
			}
		}
		if hasOverride {
			b.WriteString(" (override)")
		}
		if f.Description != "" {
			fmt.Fprintf(&b, "  # %s", f.Description)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// actionSetCoreConfig updates a core config key.
func (s *Skill) actionSetCoreConfig(ctx context.Context, sectionName, key, value string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.config == nil {
		return "Config store not available.", nil
	}

	section, ok := config.GetSection(sectionName)
	if !ok {
		return fmt.Sprintf("Unknown config section %q.", sectionName), nil
	}

	// Validate key belongs to this section.
	var validField *config.ConfigField
	for i, f := range section.Fields {
		if f.Key == key {
			validField = &section.Fields[i]
			break
		}
	}
	if validField == nil {
		var validKeys []string
		for _, f := range section.Fields {
			validKeys = append(validKeys, f.Key)
		}
		return fmt.Sprintf("Key %q is not valid for section %q. Valid keys: %s", key, sectionName, strings.Join(validKeys, ", ")), nil
	}

	// Delete override if value is empty.
	if value == "" {
		if err := s.config.Delete(ctx, key); err != nil {
			return fmt.Sprintf("Failed to delete config key %q: %v", key, err), nil
		}
		return fmt.Sprintf("Removed override for %q (reverted to default).", key), nil
	}

	updatedBy := s.resolveUsername(ctx)
	isSecret := validField.Secret
	if err := s.config.Set(ctx, key, value, updatedBy, isSecret); err != nil {
		return fmt.Sprintf("Failed to set %q: %v", key, err), nil
	}

	if isSecret {
		return fmt.Sprintf("Updated %q (secret value not displayed).", key), nil
	}
	return fmt.Sprintf("Updated %q = %q.", key, value), nil
}
