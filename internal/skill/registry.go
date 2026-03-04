package skill

import (
	"strings"
	"sync"
)

// Registry holds all registered skills and tracks which are disabled.
// By default all registered skills are enabled; individual skills can be disabled at runtime.
type Registry struct {
	mu            sync.RWMutex
	skills        map[string]Skill
	manifests     map[string]*Manifest // keyed by manifest Name
	skillManifest map[string]string    // skill name → manifest name
	configHandler map[string]string    // manifest name → skill name for ConfigReloadable dispatch
	disabled      map[string]bool
	capabilities  map[string]bool
	builtins      map[string]bool // names of built-in skills, protected from external overwrites
}

// NewRegistry creates a registry where all skills are enabled by default.
func NewRegistry() *Registry {
	return &Registry{
		skills:        make(map[string]Skill),
		manifests:     make(map[string]*Manifest),
		skillManifest: make(map[string]string),
		configHandler: make(map[string]string),
		disabled:      make(map[string]bool),
		capabilities:  make(map[string]bool),
		builtins:      make(map[string]bool),
	}
}

// SetCapabilities sets the available capabilities for capability-gated filtering.
func (r *Registry) SetCapabilities(caps []string) {
	r.mu.Lock()
	r.capabilities = make(map[string]bool, len(caps))
	for _, c := range caps {
		r.capabilities[c] = true
	}
	r.mu.Unlock()
}

// AddCapability adds a single capability at runtime (e.g. after credentials are configured).
func (r *Registry) AddCapability(cap string) {
	r.mu.Lock()
	r.capabilities[cap] = true
	r.mu.Unlock()
}

// RemoveCapability removes a capability at runtime (e.g. when credentials are revoked).
func (r *Registry) RemoveCapability(cap string) {
	r.mu.Lock()
	delete(r.capabilities, cap)
	r.mu.Unlock()
}

// Capabilities returns the currently active capabilities.
func (r *Registry) Capabilities() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	caps := make([]string, 0, len(r.capabilities))
	for c := range r.capabilities {
		caps = append(caps, c)
	}
	return caps
}

// Register adds a built-in skill to the registry (no manifest association).
func (r *Registry) Register(s Skill) {
	r.mu.Lock()
	r.skills[s.Name()] = s
	r.builtins[s.Name()] = true
	r.mu.Unlock()
}

// RegisterWithManifest adds a built-in skill and its manifest.
// The manifest is stored by m.Name (e.g. "craft"), and the first skill registered
// with a given manifest becomes its config change handler.
func (r *Registry) RegisterWithManifest(s Skill, m *Manifest) {
	r.mu.Lock()
	r.skills[s.Name()] = s
	r.builtins[s.Name()] = true
	if m != nil {
		r.manifests[m.Name] = m
		r.skillManifest[s.Name()] = m.Name
		// First skill registered with this manifest handles config changes.
		if _, exists := r.configHandler[m.Name]; !exists {
			r.configHandler[m.Name] = s.Name()
		}
	}
	r.mu.Unlock()
}

// RegisterExternalWithManifest adds an external (non-built-in) skill and its manifest.
// Unlike RegisterWithManifest, external skills can be unregistered via UnregisterSkill.
// External skills cannot overwrite a built-in skill with the same name.
func (r *Registry) RegisterExternalWithManifest(s Skill, m *Manifest) {
	r.mu.Lock()
	if r.builtins[s.Name()] {
		r.mu.Unlock()
		return // protect built-in skills from being overwritten
	}
	r.skills[s.Name()] = s
	if m != nil {
		r.manifests[m.Name] = m
		r.skillManifest[s.Name()] = m.Name
		if _, exists := r.configHandler[m.Name]; !exists {
			r.configHandler[m.Name] = s.Name()
		}
	}
	r.mu.Unlock()
}

// RegisterInGroup adds a skill that belongs to a manifest group but doesn't own the manifest.
// Used for sibling skills (e.g. craft_read belongs to the "craft" manifest group).
func (r *Registry) RegisterInGroup(s Skill, manifestName string) {
	r.mu.Lock()
	r.skills[s.Name()] = s
	if manifestName != "" {
		r.skillManifest[s.Name()] = manifestName
	}
	r.mu.Unlock()
}

// UnregisterSkill removes a skill and its manifest association from the registry.
// Built-in skills (registered via Register/RegisterWithManifest) cannot be unregistered.
func (r *Registry) UnregisterSkill(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.builtins[name] {
		return // protect built-in skills from external skill uninstall
	}
	delete(r.skills, name)
	delete(r.disabled, name)
	if mName, ok := r.skillManifest[name]; ok {
		delete(r.skillManifest, name)
		// Only remove config handler if this skill was the handler.
		if r.configHandler[mName] == name {
			delete(r.configHandler, mName)
			// Elect a new handler from remaining skills in the group.
			for sn, mn := range r.skillManifest {
				if mn == mName {
					r.configHandler[mName] = sn
					break
				}
			}
		}
		// If no more skills reference this manifest, remove it too.
		hasRefs := false
		for _, mn := range r.skillManifest {
			if mn == mName {
				hasRefs = true
				break
			}
		}
		if !hasRefs {
			delete(r.manifests, mName)
		}
	}
}

// AddManifest adds a manifest without a corresponding programmatic skill.
// Used for text-only skills that inject system prompt instructions.
func (r *Registry) AddManifest(m *Manifest) {
	if m != nil {
		r.manifests[m.Name] = m
	}
}

// Get returns a skill by name if it exists, is enabled, and has required capabilities.
func (r *Registry) Get(name string) (Skill, bool) {
	s, ok := r.skills[name]
	if !ok {
		return nil, false
	}
	r.mu.RLock()
	dis := r.disabled[name]
	r.mu.RUnlock()
	if dis || !r.hasCapabilities(s) {
		return nil, false
	}
	return s, true
}

// EnabledSkills returns all registered skills that are enabled and have required capabilities.
func (r *Registry) EnabledSkills() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []Skill
	for name, s := range r.skills {
		if !r.disabled[name] && r.hasCapabilities(s) {
			result = append(result, s)
		}
	}
	return result
}

// AllSkills returns all registered skills with their enabled/disabled status and manifest group.
func (r *Registry) AllSkills() []SkillStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []SkillStatus
	for name, s := range r.skills {
		result = append(result, SkillStatus{
			Skill:           s,
			Enabled:         !r.disabled[name],
			HasCapabilities: r.hasCapabilities(s),
			ManifestGroup:   r.skillManifest[name],
		})
	}
	return result
}

// SkillStatus holds a skill and its current status.
type SkillStatus struct {
	Skill           Skill
	Enabled         bool   // not in disabled list
	HasCapabilities bool   // all required capabilities present
	ManifestGroup   string // manifest name this skill belongs to (empty if standalone)
}

// DisableSkill marks a skill as disabled.
func (r *Registry) DisableSkill(name string) {
	r.mu.Lock()
	r.disabled[name] = true
	r.mu.Unlock()
}

// DisableGroup disables all skills belonging to a manifest group.
func (r *Registry) DisableGroup(manifestName string) {
	r.mu.Lock()
	for skillName, mName := range r.skillManifest {
		if mName == manifestName {
			r.disabled[skillName] = true
		}
	}
	r.mu.Unlock()
}

// EnableSkill re-enables a disabled skill.
func (r *Registry) EnableSkill(name string) {
	r.mu.Lock()
	delete(r.disabled, name)
	r.mu.Unlock()
}

// EnableGroup re-enables all skills belonging to a manifest group.
func (r *Registry) EnableGroup(manifestName string) {
	r.mu.Lock()
	for skillName, mName := range r.skillManifest {
		if mName == manifestName {
			delete(r.disabled, skillName)
		}
	}
	r.mu.Unlock()
}

// IsDisabled returns whether a skill is explicitly disabled.
func (r *Registry) IsDisabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.disabled[name]
}

// SkillManifest returns the manifest name for a skill, or "" if standalone.
func (r *Registry) SkillManifest(skillName string) string {
	return r.skillManifest[skillName]
}

// GroupSkills returns all skill names belonging to a manifest group.
func (r *Registry) GroupSkills(manifestName string) []string {
	var result []string
	for skillName, mName := range r.skillManifest {
		if mName == manifestName {
			result = append(result, skillName)
		}
	}
	return result
}

// SystemPrompts returns all system prompt texts from manifests of enabled skills.
func (r *Registry) SystemPrompts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var prompts []string
	for _, m := range r.manifests {
		if m.SystemPrompt == "" {
			continue
		}
		// Check if all skills in this manifest group are disabled.
		if r.isGroupDisabled(m.Name) {
			continue
		}
		prompts = append(prompts, m.SystemPrompt)
	}
	return prompts
}

// isGroupDisabled returns true if ALL skills in the group are disabled.
// If the manifest has no group members, checks by manifest name as skill name (legacy).
// Must be called with mu held.
func (r *Registry) isGroupDisabled(manifestName string) bool {
	hasMembers := false
	for skillName, mName := range r.skillManifest {
		if mName == manifestName {
			hasMembers = true
			if !r.disabled[skillName] {
				return false // at least one member is enabled
			}
		}
	}
	if !hasMembers {
		// Legacy: check if a skill with the manifest name exists and is disabled.
		return r.disabled[manifestName]
	}
	return true // all members disabled
}

// MatchForceTool checks if a message matches any manifest's ForceTriggers.
// Returns the skill name to force (not manifest name), or empty string if no match.
func (r *Registry) MatchForceTool(message string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lower := strings.ToLower(message)
	for _, m := range r.manifests {
		if len(m.ForceTriggers) == 0 || r.isGroupDisabled(m.Name) {
			continue
		}
		for _, trigger := range m.ForceTriggers {
			if strings.Contains(lower, trigger) {
				// Return the skill name (used in tool definitions), not the manifest name.
				// The configHandler maps manifest → skill name.
				if sName, ok := r.configHandler[m.Name]; ok {
					return sName
				}
				return m.Name // fallback if no handler registered
			}
		}
	}
	return ""
}

// Manifests returns all registered manifests.
func (r *Registry) Manifests() []*Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Manifest
	for _, m := range r.manifests {
		result = append(result, m)
	}
	return result
}

// GetManifest returns a manifest by name.
func (r *Registry) GetManifest(name string) (*Manifest, bool) {
	m, ok := r.manifests[name]
	return m, ok
}

// ConfigKeys returns all config keys declared by registered manifests,
// plus auto-generated skills.<name>.enabled keys for all registered skills.
func (r *Registry) ConfigKeys() []string {
	seen := make(map[string]bool)
	var keys []string
	for _, m := range r.manifests {
		for _, k := range m.ConfigKeys {
			if !seen[k] {
				keys = append(keys, k)
				seen[k] = true
			}
		}
	}
	// Add skills.<name>.enabled for each registered skill.
	for name := range r.skills {
		key := "skills." + name + ".enabled"
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	// Add skills.<manifestName>.enabled for each manifest group.
	for mName := range r.manifests {
		key := "skills." + mName + ".enabled"
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	return keys
}

// DispatchConfigChanged notifies the skill that owns the given config key.
// Skills must implement ConfigReloadable to receive notifications.
// Also handles system_prompt keys by updating the manifest directly.
func (r *Registry) DispatchConfigChanged(key, value string, deleted bool) {
	// Handle skills.<name>.enabled keys.
	if strings.HasSuffix(key, ".enabled") && strings.HasPrefix(key, "skills.") {
		parts := strings.SplitN(key, ".", 3) // skills.<name>.enabled
		if len(parts) == 3 && parts[2] == "enabled" {
			targetName := parts[1]
			enable := deleted || strings.EqualFold(value, "true") || value == "1" || value == ""
			// Check if it's a manifest group name first.
			if _, isManifest := r.manifests[targetName]; isManifest {
				if enable {
					r.EnableGroup(targetName)
				} else {
					r.DisableGroup(targetName)
				}
				return
			}
			// Individual skill toggle.
			if _, exists := r.skills[targetName]; exists {
				if enable {
					r.EnableSkill(targetName)
				} else {
					r.DisableSkill(targetName)
				}
				return
			}
		}
	}

	for mName, m := range r.manifests {
		for _, ck := range m.ConfigKeys {
			if ck != key {
				continue
			}
			// Handle system_prompt updates by mutating the manifest.
			if strings.HasSuffix(key, ".system_prompt") && !deleted {
				m.SystemPrompt = value
			}
			// Dispatch to the config handler skill if it implements ConfigReloadable.
			if handlerName, ok := r.configHandler[mName]; ok {
				if s, ok := r.skills[handlerName]; ok {
					if cr, ok := s.(ConfigReloadable); ok {
						cr.OnConfigChanged(key, value)
					}
				}
			}
			return
		}
	}
}

// SecretKeys returns a set of all secret keys declared across all manifests.
func (r *Registry) SecretKeys() map[string]bool {
	result := make(map[string]bool)
	for _, m := range r.manifests {
		for _, k := range m.SecretKeys {
			result[k] = true
		}
	}
	return result
}

// SkillConfigField describes a single config key for a skill.
type SkillConfigField struct {
	Key         string `json:"key"`
	Secret      bool   `json:"secret"`
	Value       string `json:"value,omitempty"`
	HasValue    bool   `json:"has_value"`
	HasOverride bool   `json:"has_override"`
}

// ConfigValueGetter abstracts config store lookups for the registry.
type ConfigValueGetter interface {
	GetEffective(key string) (string, bool) // override → base fallback
	HasOverride(key string) bool
	IsSecretKey(key string) bool
}

// GetSkillConfigSchema returns the config schema for a skill by manifest name.
func (r *Registry) GetSkillConfigSchema(manifestName string, getter ConfigValueGetter) ([]SkillConfigField, bool) {
	m, ok := r.GetManifest(manifestName)
	if !ok {
		return nil, false
	}

	fields := make([]SkillConfigField, 0, len(m.ConfigKeys))
	for _, key := range m.ConfigKeys {
		isSecret := getter != nil && getter.IsSecretKey(key)
		hasOverride := getter != nil && getter.HasOverride(key)

		field := SkillConfigField{
			Key:         key,
			Secret:      isSecret,
			HasOverride: hasOverride,
		}

		if isSecret {
			// Never expose secret values; only indicate whether a value exists.
			if hasOverride {
				field.HasValue = true
			} else if getter != nil {
				_, field.HasValue = getter.GetEffective(key)
			}
		} else if getter != nil {
			if val, ok := getter.GetEffective(key); ok {
				field.Value = val
				field.HasValue = true
			}
		}

		fields = append(fields, field)
	}

	return fields, true
}

// ApprovalLevelFor returns the approval level for a skill.
// Checks ApprovalDeclarer interface first, then defaults to ApprovalAuto.
func (r *Registry) ApprovalLevelFor(name string) ApprovalLevel {
	s, ok := r.skills[name]
	if !ok {
		return ApprovalAuto
	}
	if ad, ok := s.(ApprovalDeclarer); ok {
		return ad.ApprovalLevel()
	}
	return ApprovalAuto
}

// hasCapabilities checks whether the skill's required capabilities are all available.
// Skills that don't implement CapabilityAware always pass.
func (r *Registry) hasCapabilities(s Skill) bool {
	ca, ok := s.(CapabilityAware)
	if !ok {
		return true
	}
	for _, cap := range ca.RequiredCapabilities() {
		if !r.capabilities[cap] {
			return false
		}
	}
	return true
}
