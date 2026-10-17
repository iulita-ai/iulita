package todoist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iulita-ai/iulita/internal/skill"
)

// SetNativeOwner explicitly assigns this server connection; empty disables native use.
func (c *Client) SetNativeOwner(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nativeOwner = owner
	c.nativeEpoch++
}

// NativeAvailable requires the explicitly assigned owner connection.
func (s *Skill) NativeAvailable(_ context.Context, owner string) bool {
	c := s.client
	c.mu.RLock()
	defer c.mu.RUnlock()
	return owner != "" && owner == c.nativeOwner && c.apiToken != ""
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *Skill) NativePolicy() skill.NativePolicy {
	c := s.client
	c.mu.RLock()
	epoch := c.nativeEpoch
	c.mu.RUnlock()
	p := skill.NativePolicy{Revision: fmt.Sprintf("owner-todoist-v1:%d", epoch), Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{}}
	var schema struct {
		Properties map[string]struct{ Enum []string }
	}
	if json.Unmarshal(s.InputSchema(), &schema) != nil {
		return p
	}
	for _, a := range schema.Properties["action"].Enum {
		p.Actions[a] = skill.ActionPolicy{Effect: "mutating", Confirmation: true, Scopes: []string{"assistant:services:write"}}
	}
	for _, a := range []string{"list", "get", "completed", "completed_by_due_date", "filter", "projects", "archived_projects", "project_collaborators", "sections", "labels", "search_labels", "comments"} {
		p.Actions[a] = skill.ActionPolicy{Effect: "read", Scopes: []string{"assistant:services:read"}}
	}
	return p
}

// NativeExecute executes with an owner-checked, revision-pinned connection snapshot.
func (s *Skill) NativeExecute(ctx context.Context, in json.RawMessage, revision string) (string, error) {
	c := s.client
	c.mu.RLock()
	if skill.UserIDFrom(ctx) == "" || skill.UserIDFrom(ctx) != c.nativeOwner || revision != fmt.Sprintf("owner-todoist-v1:%d", c.nativeEpoch) || c.apiToken == "" {
		c.mu.RUnlock()
		return "", errors.New("todoist account unavailable")
	}
	snapshot := NewClient(c.apiToken, c.httpClient, c.logger)
	snapshot.baseURL = c.baseURL
	c.mu.RUnlock()
	return NewSkill(snapshot, s.logger).Execute(ctx, in)
}
