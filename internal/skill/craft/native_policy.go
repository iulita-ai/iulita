package craft

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
func (c *Client) nativeSnapshot(owner, revision string) (*Client, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if owner == "" || owner != c.nativeOwner || (revision != "" && revision != fmt.Sprintf("owner-craft-v1:%d", c.nativeEpoch)) || c.apiKey == "" || c.apiBaseURL == "" {
		return nil, errors.New("craft account unavailable")
	}
	return NewClient(c.apiBaseURL, c.apiKey, c.httpClient), nil
}
func (c *Client) nativeRevision() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return fmt.Sprintf("owner-craft-v1:%d", c.nativeEpoch)
}
func (c *Client) nativeAvailable(owner string) bool {
	_, err := c.nativeSnapshot(owner, "")
	return err == nil
}
func craftPolicy(c *Client, write bool) skill.NativePolicy {
	scope, effect := "assistant:services:read", "read"
	if write {
		scope, effect = "assistant:services:write", "mutating"
	}
	return skill.NativePolicy{Revision: c.nativeRevision(), Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{"": {Effect: effect, Confirmation: write, Scopes: []string{scope}}}}
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *SearchSkill) NativePolicy() skill.NativePolicy { p := craftPolicy(s.client, false); return p }

// NativeAvailable requires the explicitly assigned owner connection.
func (s *SearchSkill) NativeAvailable(_ context.Context, owner string) bool {
	return s.client.nativeAvailable(owner)
}

// NativeExecute executes with an owner-checked, revision-pinned connection snapshot.
func (s *SearchSkill) NativeExecute(ctx context.Context, in json.RawMessage, revision string) (string, error) {
	c, err := s.client.nativeSnapshot(skill.UserIDFrom(ctx), revision)
	if err != nil {
		return "", err
	}
	return NewSearch(c).Execute(ctx, in)
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *ReadSkill) NativePolicy() skill.NativePolicy { p := craftPolicy(s.client, false); return p }

// NativeAvailable requires the explicitly assigned owner connection.
func (s *ReadSkill) NativeAvailable(_ context.Context, owner string) bool {
	return s.client.nativeAvailable(owner)
}

// NativeExecute executes with an owner-checked, revision-pinned connection snapshot.
func (s *ReadSkill) NativeExecute(ctx context.Context, in json.RawMessage, revision string) (string, error) {
	c, err := s.client.nativeSnapshot(skill.UserIDFrom(ctx), revision)
	if err != nil {
		return "", err
	}
	return NewRead(c).Execute(ctx, in)
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *WriteSkill) NativePolicy() skill.NativePolicy { p := craftPolicy(s.client, true); return p }

// NativeAvailable requires the explicitly assigned owner connection.
func (s *WriteSkill) NativeAvailable(_ context.Context, owner string) bool {
	return s.client.nativeAvailable(owner)
}

// NativeExecute executes with an owner-checked, revision-pinned connection snapshot.
func (s *WriteSkill) NativeExecute(ctx context.Context, in json.RawMessage, revision string) (string, error) {
	c, err := s.client.nativeSnapshot(skill.UserIDFrom(ctx), revision)
	if err != nil {
		return "", err
	}
	return NewWrite(c).Execute(ctx, in)
}

// NativePolicy declares the reviewed native actions and authority requirements.
func (s *TasksSkill) NativePolicy() skill.NativePolicy {
	p := craftPolicy(s.client, false)
	p.Actions = map[string]skill.ActionPolicy{"list": {Effect: "read", Scopes: []string{"assistant:services:read"}}, "create": {Effect: "mutating", Confirmation: true, Scopes: []string{"assistant:services:write"}}, "complete": {Effect: "mutating", Confirmation: true, Scopes: []string{"assistant:services:write"}}}
	return p
}

// NativeAvailable requires the explicitly assigned owner connection.
func (s *TasksSkill) NativeAvailable(_ context.Context, owner string) bool {
	return s.client.nativeAvailable(owner)
}

// NativeExecute executes with an owner-checked, revision-pinned connection snapshot.
func (s *TasksSkill) NativeExecute(ctx context.Context, in json.RawMessage, revision string) (string, error) {
	c, err := s.client.nativeSnapshot(skill.UserIDFrom(ctx), revision)
	if err != nil {
		return "", err
	}
	return NewTasks(c).Execute(ctx, in)
}
