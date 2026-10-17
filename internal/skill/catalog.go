package skill

import (
	"fmt"
	"sort"

	"github.com/iulita-ai/iulita/internal/llm"
)

// ActionPolicy describes authority and effects before dispatch, independently
// of the transport. An empty action name describes a single-action skill.
type ActionPolicy struct {
	Effect       string
	Confirmation bool
	Scopes       []string
}

// NativePolicy is an explicit opt-in to owner-scoped native execution. It does
// not grant access to legacy chats, channel delivery or server-global accounts.
type NativePolicy struct {
	Revision              string
	ActionField           string // Empty means action; explicit alternatives are reviewed per skill.
	Account               bool
	Interactive           bool
	InternalConfirmations bool // Exact sanitized confirmations happen inside the reviewed executor.
	Actions               map[string]ActionPolicy
}

// NativeDeclarer is implemented only after reviewing the skill's owner lookup,
// effects and transport dependencies. Unknown/external skills fail closed.
type NativeDeclarer interface{ NativePolicy() NativePolicy }

// CatalogEntry is a copy of one currently enabled registry binding. Both
// conversation engines use the same definitions and enablement checks.
type CatalogEntry struct {
	Binding      string
	Skill        Skill
	Definition   llm.ToolDefinition
	Instructions string
	Native       *NativePolicy
}

// Catalog snapshots enabled skills and manifests under a single registry lock.
func (r *Registry) Catalog() []CatalogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CatalogEntry
	for name, s := range r.skills {
		if r.disabled[name] || !r.hasCapabilities(s) || s.InputSchema() == nil {
			continue
		}
		e := CatalogEntry{Binding: fmt.Sprintf("%s:%d", r.instance, r.bindings[name]), Skill: s, Definition: llm.ToolDefinition{Name: name, Description: s.Description(), InputSchema: append([]byte(nil), s.InputSchema()...)}}
		if m := r.manifests[r.skillManifest[name]]; m != nil {
			e.Instructions = m.SystemPrompt
		}
		if d, ok := s.(NativeDeclarer); ok && r.builtins[name] {
			p := d.NativePolicy()
			p.Actions = copyActions(p.Actions)
			e.Native = &p
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Definition.Name < out[j].Definition.Name })
	return out
}

func copyActions(in map[string]ActionPolicy) map[string]ActionPolicy {
	out := make(map[string]ActionPolicy, len(in))
	for name, a := range in {
		a.Scopes = append([]string(nil), a.Scopes...)
		out[name] = a
	}
	return out
}

// NativeBinding ties reviewed adapters to the exact registered built-in object.
// A restart or replacement invalidates queued snapshots rather than silently
// rebinding them to newly configured credentials/clients.
func (r *Registry) NativeBinding(s Skill) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name := s.Name()
	if r.skills[name] != s || !r.builtins[name] || r.disabled[name] || !r.hasCapabilities(s) {
		return "", false
	}
	return fmt.Sprintf("%s:%d", r.instance, r.bindings[name]), true
}
