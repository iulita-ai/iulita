package engine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// Catalog resolves current owner-scoped tools. It must not read credentials into
// the model context. The engine pins definitions/policies at admission and checks
// the live binding again before dispatch; newly enabled tools need a new turn.
type Catalog func(context.Context, string) ([]Tool, error)

// CatalogFactory binds a live catalog to one fenced execution repository.
type CatalogFactory func(*Engine) Catalog

func copyTool(t Tool) (Tool, error) {
	if t.Definition.Name == "" || t.Revision == "" || t.Execute == nil || (t.Effect != "read" && t.Effect != "mutating" && t.Effect != "unknown") {
		return Tool{}, errors.New("invalid native tool binding")
	}
	if _, single := t.Actions[""]; single && len(t.Actions) != 1 {
		return Tool{}, errors.New("mixed default and named action policy")
	}
	actions := map[string]skill.ActionPolicy{}
	for name, a := range t.Actions {
		if len(name) > 128 || (a.Effect != "read" && a.Effect != "mutating" && a.Effect != "unknown") {
			return Tool{}, errors.New("invalid native action policy")
		}
		a.Scopes = append([]string(nil), a.Scopes...)
		actions[name] = a
	}
	t.Actions = actions
	t.RequiredScopes = append([]string(nil), t.RequiredScopes...)
	t.Definition.InputSchema = append([]byte(nil), t.Definition.InputSchema...)
	return t, nil
}

type operationKey struct{}

// OperationID is the semantic operation identity assigned by the fenced engine,
// never an argument supplied by the model. Local atomic adapters journal their
// result in the same transaction as their effect.
func OperationID(ctx context.Context) string {
	id, ok := ctx.Value(operationKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

func (e *Engine) executeTool(ctx context.Context, f conversations.Fence, t Tool, id string, input json.RawMessage, approval bool) (string, error) {
	if approval {
		// Keep the approving session live during external I/O as well. A
		// revoked approval cancels the request; its effect may already exist,
		// so the normal ambiguous-outcome journal still forbids replay.
		guarded, cancel := e.Repo.TurnContext(ctx, f)
		defer cancel()
		ctx = guarded
	}
	bridge := turnInteraction{e: e, f: f, a: AuthorityFrom(ctx)}
	ctx = interact.WithPrompter(ctx, bridge)
	ctx = interact.WithConfirmer(ctx, bridge)
	return t.Execute(context.WithValue(ctx, operationKey{}, id), f, input)
}

func (e *Engine) toolsFor(ctx context.Context, owner string) (map[string]Tool, error) {
	out := make(map[string]Tool, len(e.tools))
	for name := range e.tools {
		out[name] = e.tools[name]
	}
	if e.Catalog == nil {
		return out, nil
	}
	extra, err := e.Catalog(ctx, owner)
	if err != nil {
		return nil, err
	}

	for i := range extra {
		t := extra[i]
		t, err = copyTool(t)
		if err != nil {
			return nil, err
		}
		if _, exists := out[t.Definition.Name]; exists {
			return nil, errors.New("duplicate native catalog binding")
		}
		out[t.Definition.Name] = t
	}

	return out, nil
}

func hasScopes(a Authority, required []string) bool {
	for _, r := range required {
		found := false
		for _, s := range a.Scopes {
			if r == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func actionPolicy(a Authority, t Tool, input json.RawMessage) (skill.ActionPolicy, bool) {
	if len(t.Actions) == 0 {
		return skill.ActionPolicy{Effect: t.Effect}, true
	}
	name := ""
	if _, single := t.Actions[""]; !single {
		var v map[string]json.RawMessage
		field := t.ActionField
		if field == "" {
			field = "action"
		}
		if json.Unmarshal(input, &v) != nil || json.Unmarshal(v[field], &name) != nil {
			return skill.ActionPolicy{}, false
		}
	}
	p, ok := t.Actions[name]
	return p, ok && hasScopes(a, p.Scopes)
}

// Capabilities returns the tools permitted for this caller and conversation,
// using the exact same filtering as admission. No account IDs/tokens or prompts.
func (e *Engine) Capabilities(ctx context.Context, owner string, c conversations.Conversation) ([]Tool, error) {
	if c.OwnerID != owner || owner == "" {
		return nil, conversations.ErrNotFound
	}
	if AuthorityFrom(ctx).PrincipalType == "service" && c.Settings.MemoryMode != "isolated" {
		return nil, conversations.ErrInvalid
	}
	tools, err := e.toolsFor(ctx, owner)
	if err != nil {
		return nil, err
	}
	var out []Tool
	for name := range tools {
		t := tools[name]
		if permits(AuthorityFrom(ctx), t) && (c.Settings.MemoryMode != "isolated" || t.Isolated) {
			out = append(out, t)
		}
	}
	return out, nil
}

func successfulOutcome(result string) bool {
	var v struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(result), &v) == nil {
		switch v.Status {
		case "lookup_failed", "invalid_input", "rejected", "unavailable":
			return false
		}
	}
	return true
}
