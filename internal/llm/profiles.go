package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/iulita-ai/iulita/internal/models"
)

// ProfileBinding is a server-built client with immutable model parameters.
// Eligibility must come from local compatibility rules and server-owned probe
// evidence, never a settings request. Experimental profiles are probe-only.
type ProfileBinding struct {
	Profile       models.Profile
	Provider      Provider
	Eligibility   string
	Images        bool
	ContextTokens int
}

// ProfileSnapshot holds one immutable policy revision. Clients shared by
// profiles share the connection transport, not mutable model parameters.
type ProfileSnapshot struct {
	revision uint64
	profiles map[string]ProfileBinding
	policy   models.Policy
}

func NewProfileSnapshot(revision uint64, settings models.Settings, bindings map[string]ProfileBinding) (*ProfileSnapshot, error) {
	if errs := settings.Validate(); len(errs) > 0 {
		return nil, errs[0]
	}
	if settings.Policy.Everyday == "" {
		return nil, fmt.Errorf("everyday profile is required for activation")
	}
	// Future fallback policy is not silently ignored in the first resolver unit.
	for _, ids := range settings.Policy.Fallbacks {
		if len(ids) > 0 {
			return nil, fmt.Errorf("profile fallbacks require protocol verification")
		}
	}
	s := &ProfileSnapshot{revision: revision, profiles: map[string]ProfileBinding{}, policy: settings.Policy}
	s.policy.LegacyHints = map[string]string{}
	for hint, role := range settings.Policy.LegacyHints {
		s.policy.LegacyHints[hint] = role
	}
	s.policy.LegacyProfileHints = map[string]string{}
	for hint, id := range settings.Policy.LegacyProfileHints {
		s.policy.LegacyProfileHints[hint] = id
	}
	s.policy.ForbiddenProviders = append([]string(nil), settings.Policy.ForbiddenProviders...)
	for _, profile := range settings.Profiles {
		b, ok := bindings[profile.ID]
		if !ok {
			b = ProfileBinding{Profile: profile, Eligibility: "experimental"}
		}
		if b.Profile != profile {
			return nil, fmt.Errorf("profile %q parameters do not match client", profile.ID)
		}
		if d, known := models.Lookup(profile.Connection, profile.Model); known {
			b.Images = b.Images && d.Images
			b.ContextTokens = d.ContextTokens
		}
		s.profiles[profile.ID] = b
	}
	for role, id := range settings.Policy.Roles() {
		if id != "" {
			if err := s.eligible(id); err != nil {
				return nil, err
			}
			if role == "vision" && !s.profiles[id].Images {
				return nil, fmt.Errorf("vision profile requires verified image support")
			}
		}
	}
	if settings.Policy.Classifier.Enabled {
		if err := s.eligible(settings.Policy.Classifier.Profile); err != nil {
			return nil, err
		}
	}
	for _, id := range settings.Policy.LegacyProfileHints {
		if err := s.eligible(id); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *ProfileSnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}
func (s *ProfileSnapshot) eligible(id string) error {
	b, ok := s.profiles[id]
	if !ok {
		return fmt.Errorf("unknown model profile %q", id)
	}
	if b.Provider == nil {
		return fmt.Errorf("profile %q has no configured connection", id)
	}
	if b.Eligibility != "production_eligible" && b.Eligibility != "legacy_preserved" {
		return fmt.Errorf("model profile %q requires verification", id)
	}
	for _, provider := range s.policy.ForbiddenProviders {
		if b.Profile.Connection == provider {
			return fmt.Errorf("model profile provider is forbidden")
		}
	}
	return nil
}
func (s *ProfileSnapshot) resolve(req Request) (ProfileBinding, Request, string, error) {
	role := "everyday"
	if req.Role != "" {
		role = req.Role
		if _, ok := s.policy.Roles()[role]; !ok {
			return ProfileBinding{}, req, "", fmt.Errorf("unknown task role")
		}
	}
	id := req.ProfileID
	if id != "" {
		if _, ok := s.profiles[id]; !ok {
			return ProfileBinding{}, req, "", fmt.Errorf("unknown model profile %q", id)
		}
	} else {
		if target, ok := s.policy.LegacyProfileHints[req.RouteHint]; ok {
			id = target
		}
		if req.Role != "" {
			role = req.Role
		} else if mapped, ok := s.policy.LegacyHints[req.RouteHint]; ok {
			role = mapped
		} else if req.RouteHint == RouteHintCheap {
			role = "background"
		} else if req.RouteHint == RouteHintVision {
			role = "vision"
		} else if _, ok := s.policy.Roles()[req.RouteHint]; ok {
			role = req.RouteHint
		} else if id == "" && strings.HasPrefix(req.Message, "hint:") {
			parts := strings.SplitN(req.Message[5:], " ", 2)
			if target, ok := s.policy.LegacyProfileHints[strings.TrimSpace(parts[0])]; ok {
				id = target
				if len(parts) == 2 {
					req.Message = parts[1]
				} else {
					req.Message = ""
				}
			}
			if mapped, ok := s.policy.LegacyHints[strings.TrimSpace(parts[0])]; ok {
				role = mapped
				if len(parts) == 2 {
					req.Message = parts[1]
				} else {
					req.Message = ""
				}
			}
		}
		if id == "" {
			var ok bool
			id, ok = s.policy.Roles()[role]
			if !ok {
				return ProfileBinding{}, req, "", fmt.Errorf("unknown task role")
			}
		}
		// An unassigned optional background/complex role uses everyday explicitly.
		if id == "" && role != "vision" {
			id = s.policy.Everyday
		}
	}
	if err := s.eligible(id); err != nil {
		return ProfileBinding{}, req, "", err
	}
	b := s.profiles[id]
	if len(req.Images) > 0 && !b.Images {
		if len(req.ToolExchanges) > 0 {
			return ProfileBinding{}, req, "", fmt.Errorf("cannot change model during an image tool loop")
		}
		id = s.policy.Vision
		role = "vision"
		if id == "" {
			return ProfileBinding{}, req, "", fmt.Errorf("no verified vision profile configured")
		}
		if err := s.eligible(id); err != nil {
			return ProfileBinding{}, req, "", err
		}
		b = s.profiles[id]
		if !b.Images {
			return ProfileBinding{}, req, "", fmt.Errorf("profile does not support images")
		}
	}
	if len(req.Documents) > 0 && b.Profile.Connection != "claude" {
		return ProfileBinding{}, req, "", fmt.Errorf("model profile does not support document attachments")
	}
	// A completed tool round must remain on its original profile. Synthesis
	// transitions need a separate, verified terminal handoff contract.
	for _, exchange := range req.ToolExchanges {
		if exchange.ProfileID != "" && exchange.ProfileID != id {
			return ProfileBinding{}, req, "", fmt.Errorf("tool continuation belongs to another model profile")
		}
	}
	req.ProfileID = id
	req.Role = role
	req.CacheIdentity = fmt.Sprintf("profile:%s:revision:%d", id, s.revision)
	req.PolicyRevision = s.revision
	if b.Profile.Thinking == "disabled" {
		req.ThinkingBudget = 0
	}
	return b, req, role, nil
}

// ProfileInvoker exposes profile resolution to consumers without a second
// dispatcher. The existing RoutingProvider owns publication and invocation.
type ProfileInvoker interface{ AcquireProfileSnapshot() *ProfileSnapshot }

func (p *RoutingProvider) AcquireProfileSnapshot() *ProfileSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshot
}
func (p *RoutingProvider) SetProfileSnapshot(s *ProfileSnapshot) error {
	if s == nil {
		return fmt.Errorf("nil profile snapshot")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot != nil && s.revision <= p.snapshot.revision {
		return fmt.Errorf("model revision must increase")
	}
	p.snapshot = s
	return nil
}
func (p *RoutingProvider) completeProfile(ctx context.Context, req Request, callback StreamCallback, stream bool) (Response, bool, error) {
	s := req.RoutingSnapshot
	if s == nil {
		s = p.AcquireProfileSnapshot()
	}
	if s == nil {
		if req.ProfileID != "" {
			return Response{}, true, fmt.Errorf("unknown model profile %q", req.ProfileID)
		}
		return Response{}, false, nil
	}
	b, req, role, err := s.resolve(req)
	if err != nil {
		return Response{}, true, err
	}
	// Current forbidden-provider policy takes precedence over a pinned snapshot.
	current := p.AcquireProfileSnapshot()
	if current != nil {
		for _, provider := range current.policy.ForbiddenProviders {
			if provider == b.Profile.Connection {
				return Response{}, true, fmt.Errorf("model profile provider is forbidden")
			}
		}
	}
	var resp Response
	if sp, ok := b.Provider.(StreamingProvider); stream && ok {
		resp, err = sp.CompleteStream(ctx, req, callback)
	} else {
		resp, err = b.Provider.Complete(ctx, req)
	}
	resp.ProfileID = b.Profile.ID
	resp.Role = role
	resp.PolicyRevision = s.revision
	if resp.RequestedModel == "" {
		resp.RequestedModel = b.Profile.Model
	}
	return resp, true, err
}

// ContextWindow resolves only local profile metadata; it never invokes a model.
func (s *ProfileSnapshot) ContextWindow(req Request) int {
	if s == nil {
		return 0
	}
	b, _, _, err := s.resolve(req)
	if err != nil {
		return 0
	}
	return b.ContextTokens
}
