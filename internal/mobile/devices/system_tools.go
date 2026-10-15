package devices

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
)

func (s *Store) submit(ctx context.Context, f conversations.Fence, draft Draft) (string, error) {
	authority := engine.AuthorityFrom(ctx)
	draft.Viewer = authority.DeviceID
	draft.CallerFamily = authority.FamilyID
	if err := draft.validate(); err != nil {
		return "", engine.Rejection{}
	}
	selected, lookupErr := s.auth.Device(ctx, f.OwnerID, draft.Device)
	if lookupErr != nil || selected.Revoked || !selected.Activated {
		return "", engine.Rejection{}
	}
	if draft.Kind != v1.ActionKind_ACTION_KIND_READ_STATUS {
		scope := "devices:read"
		fresh := false
		if draft.Kind > v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
			scope = "devices:manage"
			fresh = true
		}
		prompt, err := s.chats.OpenPrompt(ctx, f, conversations.Prompt{Kind: "confirmation", OperationID: draft.ID, Fingerprint: draft.Fingerprint(f), RequiredScopes: []string{scope}, FreshAuthentication: fresh, Preview: "Разрешить задачу " + draft.Kind.String() + " для устройства «" + selected.Label + "»? Данные останутся в выбранном мобильном клиенте; разрешения Android и локальное подтверждение действия проверяются отдельно.", ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			return "", err
		}
		timer := time.NewTicker(100 * time.Millisecond)
		defer timer.Stop()
		for prompt.State == "open" {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-timer.C:
				prompt, err = s.chats.Prompt(ctx, f.OwnerID, f.ConversationID, f.TurnID, prompt.ID)
				if err != nil {
					return "", err
				}
			}
		}
		if prompt.State != "answered" || prompt.Decision != "approve" {
			return "", engine.Rejection{}
		}
	}
	task, err := s.Create(ctx, f, draft)
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBusy) || errors.Is(err, conversations.ErrFenced) {
			return "", engine.Rejection{}
		}
		return "", err
	}
	body, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		TaskID string `json:"task_id"`
		State  string `json:"state"`
	}{task.ID, task.State})
	return string(body), nil
}
func (s *Store) actionTool() engine.Tool {
	return engine.Tool{Definition: llm.ToolDefinition{Name: "phone_action", Description: "Request a typed torch or navigation action on an explicitly selected phone, with per-task confirmation and native permission. No shell, payments, alarms, security settings or arbitrary UI.", InputSchema: json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string","format":"uuid"},"action":{"enum":["torch","navigation"]},"enabled":{"type":"boolean"},"latitude":{"type":"number","minimum":-90,"maximum":90},"longitude":{"type":"number","minimum":-180,"maximum":180},"label":{"type":"string","maxLength":200}},"required":["device_id","action"],"additionalProperties":false}`)}, Revision: PolicyRevision, Effect: "mutating", Isolated: true, RequiredScopes: []string{"devices:manage"}, FreshAuth: true, Execute: func(ctx context.Context, f conversations.Fence, body json.RawMessage) (string, error) {
		var input struct {
			Device    string   `json:"device_id"`
			Action    string   `json:"action"`
			Enabled   *bool    `json:"enabled"`
			Latitude  *float64 `json:"latitude"`
			Longitude *float64 `json:"longitude"`
			Label     string   `json:"label"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !validID(input.Device) {
			return "", engine.Rejection{}
		}
		canonical, _ := json.Marshal(input) //nolint:errcheck // This internal snapshot contains only JSON-supported values.
		draft := Draft{ID: uuid.NewSHA1(uuid.NameSpaceOID, append([]byte(f.TurnID+"\x00phone_action\x00"), canonical...)).String(), Device: input.Device}
		switch {
		case input.Action == "torch" && input.Enabled != nil && input.Latitude == nil && input.Longitude == nil && input.Label == "":
			draft.Kind = v1.ActionKind_ACTION_KIND_SET_TORCH
			draft.System = &v1.SystemAction{Body: &v1.SystemAction_TorchEnabled{TorchEnabled: *input.Enabled}}
		case input.Action == "navigation" && input.Latitude != nil && input.Longitude != nil && input.Enabled == nil:
			draft.Kind = v1.ActionKind_ACTION_KIND_OPEN_NAVIGATION
			draft.System = &v1.SystemAction{Body: &v1.SystemAction_Navigation{Navigation: &v1.NavigationDestination{Latitude: *input.Latitude, Longitude: *input.Longitude, Label: input.Label}}}
		default:
			return "", engine.Rejection{}
		}
		return s.submit(ctx, f, draft)
	}}
}
func (s *Store) statusTool() engine.Tool {
	return engine.Tool{Definition: llm.ToolDefinition{Name: "phone_task_status", Description: "Read truthful metadata for a phone task originating in this conversation. Never returns phone content.", InputSchema: json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","format":"uuid"}},"required":["task_id"],"additionalProperties":false}`)}, Revision: PolicyRevision, Effect: "read", Isolated: true, RequiredScopes: []string{"devices:read"}, Execute: func(ctx context.Context, f conversations.Fence, body json.RawMessage) (string, error) {
		var input struct {
			Task string `json:"task_id"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !validID(input.Task) {
			return "", engine.Rejection{}
		}
		var task Task
		err := s.accounts.Guard(ctx, f.OwnerID, func(a browseroauth.Account) error {
			if a.Disabled || a.MustChangePassword {
				return ErrUnavailable
			}
			if err := s.chats.DispatchAllowed(ctx, f); err != nil {
				return err
			}
			var err error
			task, err = scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND conversation_id=?`, input.Task, f.OwnerID, f.ConversationID))
			return err
		})
		if err != nil {
			return "", engine.Rejection{}
		}
		encoded, _ := json.Marshal(struct { //nolint:errcheck // This internal snapshot contains only JSON-supported values.
			TaskID    string `json:"task_id"`
			State     string `json:"state"`
			Cancelled bool   `json:"cancellation_requested"`
		}{task.ID, task.State, task.Cancelled})
		return string(encoded), nil
	}}
}
