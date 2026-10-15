package devices

import (
	"context"
	"encoding/json"

	"strings"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
)

// Tools expose task metadata ONLY. Phone content is never returned to ordinary
// LLM tool exchanges, background analysis, global events or generic artifacts.
func (s *Store) Tools() []engine.Tool {
	var out []engine.Tool
	for name, kind := range map[string]v1.ActionKind{"phone_status": v1.ActionKind_ACTION_KIND_READ_STATUS, "phone_location": v1.ActionKind_ACTION_KIND_READ_LOCATION, "phone_notifications": v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS} {
		name, kind := name, kind
		out = append(out, engine.Tool{Definition: llm.ToolDefinition{Name: name, Description: "Create a phone task for an explicitly selected owned device. Returns task ID/status only; content is visible separately in the mobile app.", InputSchema: json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string","format":"uuid"}},"required":["device_id"],"additionalProperties":false}`)}, Revision: PolicyRevision, Effect: "mutating", Isolated: true, RequiredScopes: []string{"devices:read"}, Execute: func(ctx context.Context, f conversations.Fence, body json.RawMessage) (string, error) {
			var input struct {
				Device string `json:"device_id"`
			}
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || !validID(input.Device) {
				return "", engine.Rejection{}
			}
			taskID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(f.TurnID+"\x00"+name+"\x00"+input.Device)).String()
			draft := Draft{ID: taskID, Device: input.Device, Kind: kind}
			return s.submit(ctx, f, draft)
		}})
	}
	out = append(out, s.actionTool(), s.statusTool())
	return out
}
