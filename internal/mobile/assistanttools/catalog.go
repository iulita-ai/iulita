// Package assistanttools adapts explicitly reviewed account skills to native
// turns. It never fabricates a Telegram chat, changes legacy namespace guards,
// or exports phone observations to the general assistant pipeline.
package assistanttools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	httpengine "github.com/iulita-ai/iulita/internal/httpfetch"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/publictools"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/askuser"
	"github.com/iulita-ai/iulita/internal/skill/craft"
	"github.com/iulita-ai/iulita/internal/skill/google"
	httpskill "github.com/iulita-ai/iulita/internal/skill/httpfetch"
	"github.com/iulita-ai/iulita/internal/skill/slack"
	"github.com/iulita-ai/iulita/internal/skill/slackpost"
	"github.com/iulita-ai/iulita/internal/skill/todoist"
)

type nativeExecutor interface {
	NativeExecute(context.Context, json.RawMessage, string) (string, error)
}

type availability interface {
	NativeAvailable(context.Context, string) bool
}

// Catalog is evaluated at admission and before dispatch, not only at startup.
func Catalog(registry *skill.Registry, memoryTriggers ...string) engine.CatalogFactory {
	memoryTriggers = append([]string(nil), memoryTriggers...)
	return func(e *engine.Engine) engine.Catalog {
		e.SelectRequiredTool = func(text string) string {
			if skill.MatchMemoryTrigger(text, memoryTriggers) {
				return "remember"
			}
			if registry != nil {
				tool, _ := registry.MatchForceTrigger(text)
				return tool
			}
			return ""
		}

		return func(ctx context.Context, owner string) ([]engine.Tool, error) {
			out := publictools.Tools(registry)
			if registry == nil {
				return out, nil
			}
			a := engine.AuthorityFrom(ctx)
			for _, entry := range registry.Catalog() {
				if entry.Native == nil {
					continue
				}
				p := *entry.Native
				s := entry.Skill
				name := entry.Definition.Name
				if p.Account && (a.OwnerID != owner || a.PrincipalType == "service") {
					continue
				}
				if available, ok := s.(availability); ok && !available.NativeAvailable(ctx, owner) {
					continue
				}
				t := engine.Tool{InternalConfirmations: p.InternalConfirmations, ActionField: p.ActionField, Definition: entry.Definition, Revision: p.Revision + ":" + entry.Binding, Effect: "unknown", Isolated: !p.Account, InteractiveOnly: p.Interactive, Instructions: entry.Instructions, Actions: p.Actions, RequiredScopes: []string{"chats:write"}}
				t.Actions = permittedActions(a, p.Actions)
				if len(t.Actions) == 0 {
					continue
				}
				if len(p.Actions) == 1 {
					if action, ok := p.Actions[""]; ok {
						t.Effect = action.Effect
					}
				}
				switch name {
				case "forget":
					t.Definition.Description = "Delete one saved account fact by its exact ID after confirmation. Use recall first to identify it; never delete by an evolving search query."
					t.Definition.InputSchema = json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","minimum":1}},"required":["id"],"additionalProperties":false}`)
				case "reminders":
					t.Definition.Description += " Native list is paginated by ID: pass next_after_id as after_id while more=true."
					var schema map[string]json.RawMessage
					var properties map[string]json.RawMessage
					if json.Unmarshal(t.Definition.InputSchema, &schema) != nil || json.Unmarshal(schema["properties"], &properties) != nil {
						return nil, errors.New("invalid reminder schema")
					}
					properties["after_id"] = json.RawMessage(`{"type":"integer","minimum":0,"description":"Native list continuation from next_after_id; omit for first page"}`)
					var schemaErr error
					schema["properties"], schemaErr = json.Marshal(properties)
					if schemaErr != nil {
						return nil, schemaErr
					}
					t.Definition.InputSchema, schemaErr = json.Marshal(schema)
					if schemaErr != nil {
						return nil, schemaErr
					}
				case "session_search":
					t.Definition.Description = "Search previous messages in this conversation only. Other chats keep separate histories, including in account memory mode."
				}
				if _, single := t.Actions[""]; !single {
					var schema map[string]json.RawMessage
					var properties map[string]json.RawMessage
					var action map[string]json.RawMessage
					if json.Unmarshal(t.Definition.InputSchema, &schema) != nil || json.Unmarshal(schema["properties"], &properties) != nil || json.Unmarshal(properties[actionField(p)], &action) != nil {
						return nil, errors.New("invalid native action schema")
					}
					var allowed []string
					for name := range t.Actions {
						allowed = append(allowed, name)
					}
					sort.Strings(allowed)
					var marshalErr error
					action["enum"], marshalErr = json.Marshal(allowed)
					if marshalErr != nil {
						return nil, marshalErr
					}
					properties[actionField(p)], marshalErr = json.Marshal(action)
					if marshalErr != nil {
						return nil, marshalErr
					}
					schema["properties"], marshalErr = json.Marshal(properties)
					if marshalErr != nil {
						return nil, marshalErr
					}
					t.Definition.InputSchema, marshalErr = json.Marshal(schema)
					if marshalErr != nil {
						return nil, marshalErr
					}
				}
				schema := append(json.RawMessage(nil), t.Definition.InputSchema...)
				t.Validate = func(input json.RawMessage) bool { return validNativeInput(name, schema, input, a) }
				t.Execute = func(ctx context.Context, f conversations.Fence, input json.RawMessage) (string, error) {
					current, enabled := registry.Get(name)
					if !enabled || current != s || (p.Account && (engine.AuthorityFrom(ctx).OwnerID != f.OwnerID || engine.AuthorityFrom(ctx).PrincipalType == "service")) {
						return "", engine.Rejection{}
					}
					if available, ok := s.(availability); ok && !available.NativeAvailable(ctx, f.OwnerID) {
						return "", engine.Rejection{}
					}
					if !validNativeInput(name, schema, input, engine.AuthorityFrom(ctx)) {
						return "", engine.Rejection{}
					}
					switch name {
					case "remember", "recall", "forget", "directives":
						var in conversations.KnowledgeInput
						if json.Unmarshal(input, &in) != nil {
							return "", engine.Rejection{}
						}
						return e.Repo.AccountKnowledge(ctx, f, engine.OperationID(ctx), name, in)
					case "reminders":
						var in conversations.ReminderInput
						if json.Unmarshal(input, &in) != nil {
							return "", engine.Rejection{}
						}
						return e.Repo.Reminders(ctx, f, engine.OperationID(ctx), in, conversations.ApprovalAuthority(engine.AuthorityFrom(ctx)))
					case "session_search":
						var in conversations.KnowledgeInput
						if json.Unmarshal(input, &in) != nil {
							return "", engine.Rejection{}
						}
						return e.Repo.SearchCurrentHistory(ctx, f, in.Query, in.Limit)
					}
					// Owner only. Deliberately no legacy ChatID or fallback identity.
					ctx = skill.WithUserID(ctx, f.OwnerID)
					ctx = google.WithAccountPrincipal(ctx, f.OwnerID)
					if name == "http_request" {
						ctx = httpengine.WithScope(ctx, httpengine.Scope{UserID: f.OwnerID, NativeConversation: f.ConversationID, NativeGeneration: f.Generation, ChannelInstance: "mobile:" + f.ConversationID, Caller: httpengine.Caller{ID: "http_request", Kind: httpengine.CallerTool}, Mode: httpengine.ModeInteractive})
					}
					callCtx, cancel := context.WithTimeout(ctx, nativeTimeout(p))
					defer cancel()
					callCtx, stopBinding := watchBinding(callCtx, registry, s, p, f.OwnerID)
					defer stopBinding()
					var result string
					var err error
					if executor, ok := s.(nativeExecutor); ok {
						result, err = executor.NativeExecute(callCtx, input, p.Revision)
					} else {
						result, err = s.Execute(callCtx, input)
					}
					if err == nil && callCtx.Err() != nil {
						err = callCtx.Err()
					}
					if err != nil {
						if ctx.Err() != nil {
							return "", ctx.Err()
						}
						var v map[string]json.RawMessage
						if decodeErr := json.Unmarshal(input, &v); decodeErr != nil {
							return "", errors.New("account action outcome could not be verified")
						}
						var actionName string
						if decodeErr := json.Unmarshal(v[actionField(p)], &actionName); decodeErr != nil && len(p.Actions) > 1 {
							return "", errors.New("account action outcome could not be verified")
						}
						action, ok := p.Actions[actionName]
						if !ok {
							action = p.Actions[""]
						}
						if action.Effect == "read" {
							return `{"status":"lookup_failed","message":"Account lookup failed; do not invent a result or infer that the phone is offline."}`, nil
						}
						return "", errors.New("account action outcome could not be verified")
					}
					if name == "http_request" {
						return compactHTTPResult(result)
					}
					return bounded(result), nil
				}
				// These owner-scoped built-ins are the reviewed direct adapters.
				// Everything else with a future declaration still needs a dispatcher.
				switch name {
				case "remember", "recall", "forget", "directives", "session_search", "exchange_rate", "reminders":
					out = append(out, t)
				default:
					switch s.(type) {
					case *google.MailSkill, *google.CalendarSkill, *google.ContactsSkill, *google.TasksSkill, *craft.SearchSkill, *craft.ReadSkill, *craft.WriteSkill, *craft.TasksSkill, *todoist.Skill, *slack.SearchSkill, *slackpost.PostSkill, *httpskill.Skill, *askuser.Skill:
						out = append(out, t)
					}
				}
			}
			return out, nil
		}
	}
}

func permittedActions(a engine.Authority, in map[string]skill.ActionPolicy) map[string]skill.ActionPolicy {
	out := map[string]skill.ActionPolicy{}
	for name, p := range in {
		allowed := true
		for _, required := range p.Scopes {
			found := false
			for _, scope := range a.Scopes {
				if scope == required {
					found = true
					break
				}
			}
			if !found {
				allowed = false
				break
			}
		}
		if allowed {
			out[name] = p
		}
	}
	return out
}

func bounded(value string) string {
	if len(value) <= 48<<10 {
		return value
	}
	value = value[:48<<10]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "\n[truncated]"
}

func validInput(name string, schema, input json.RawMessage) bool {
	if !validShape(schema, input) {
		return false
	}
	var in conversations.KnowledgeInput
	switch name {
	case "directives":
		return json.Unmarshal(input, &in) == nil && (in.Action != "set" || (strings.TrimSpace(in.Content) != "" && len(in.Content) <= 16<<10))
	case "remember":
		return json.Unmarshal(input, &in) == nil && strings.TrimSpace(in.Content) != "" && len(in.Content) <= 16<<10
	case "recall", "session_search":
		return json.Unmarshal(input, &in) == nil && strings.TrimSpace(in.Query) != "" && len(in.Query) <= 2000 && in.Limit >= 0 && in.Limit <= 20
	case "reminders":
		var r conversations.ReminderInput
		if json.Unmarshal(input, &r) != nil {
			return false
		}
		switch r.Action {
		case "create":
			_, err := conversations.ReminderTime(r)
			return err == nil && strings.TrimSpace(r.Title) != "" && len(r.Title) <= 3000
		case "list":
			return r.AfterID >= 0
		case "delete":
			return r.ID > 0
		}
		return false
	case "forget":
		return json.Unmarshal(input, &in) == nil && in.ID > 0
	}
	return true
}

func actionField(p skill.NativePolicy) string {
	if p.ActionField != "" {
		return p.ActionField
	}
	return "action"
}

func nativeTimeout(p skill.NativePolicy) time.Duration {
	if p.Interactive {
		return 5 * time.Minute
	}
	return 35 * time.Second
}

func validNativeInput(name string, schema, input json.RawMessage, a engine.Authority) bool {
	if name != "http_request" {
		return validInput(name, schema, input)
	}
	plan, err := httpengine.ParsePlan(input)
	if err != nil {
		return false
	}
	steps := plan.Steps
	if plan.Request != nil {
		steps = []httpengine.Request{*plan.Request}
	}
	for n := range steps {
		step := &steps[n]
		switch strings.ToUpper(step.Method) {
		case "", "GET", "HEAD", "OPTIONS":
		default:
			found := false
			for _, scope := range a.Scopes {
				if scope == "assistant:services:write" {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
