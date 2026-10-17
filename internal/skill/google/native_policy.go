package google

import (
	"context"

	"github.com/iulita-ai/iulita/internal/skill"
)

// NativePolicy declares the reviewed native execution policy.
func (*MailSkill) NativePolicy() skill.NativePolicy {
	return googleReadPolicy("unread", "search", "read", "summary")
}

// NativePolicy declares the reviewed native execution policy.
func (*CalendarSkill) NativePolicy() skill.NativePolicy {
	return googleReadPolicy("today", "tomorrow", "week", "search", "event")
}

// NativePolicy declares the reviewed native execution policy.
func (*ContactsSkill) NativePolicy() skill.NativePolicy {
	return googleReadPolicy("search", "birthdays", "detail")
}

// NativePolicy declares the reviewed native execution policy.
func (*TasksSkill) NativePolicy() skill.NativePolicy {
	p := googleReadPolicy("lists", "list")
	for _, a := range []string{"create", "complete", "delete"} {
		p.Actions[a] = skill.ActionPolicy{Effect: "mutating", Confirmation: true, Scopes: []string{"assistant:services:write"}}
	}
	return p
}
func googleReadPolicy(actions ...string) skill.NativePolicy {
	p := skill.NativePolicy{Revision: "owner-google-v1", Account: true, Interactive: true, Actions: map[string]skill.ActionPolicy{}}
	for _, a := range actions {
		p.Actions[a] = skill.ActionPolicy{Effect: "read", Scopes: []string{"assistant:services:read"}}
	}
	return p
}

// NativeAvailable requires a stored connection belonging to this owner.
func (s *MailSkill) NativeAvailable(ctx context.Context, owner string) bool {
	return s.client.ownedAccountsAvailable(ctx, owner)
}

// NativeAvailable requires a stored connection belonging to this owner.
func (s *CalendarSkill) NativeAvailable(ctx context.Context, owner string) bool {
	return s.client.ownedAccountsAvailable(ctx, owner)
}

// NativeAvailable requires a stored connection belonging to this owner.
func (s *ContactsSkill) NativeAvailable(ctx context.Context, owner string) bool {
	return s.client.ownedAccountsAvailable(ctx, owner)
}

// NativeAvailable requires a stored connection belonging to this owner.
func (s *TasksSkill) NativeAvailable(ctx context.Context, owner string) bool {
	return s.client.ownedAccountsAvailable(ctx, owner)
}
