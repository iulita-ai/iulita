package transport

import (
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestExplicitPhoneStatusCreatesRealTaskFromFrozenTarget(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{})
	token, device := h.login(t, "owner")
	n := startNative(t, h, token, &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)), PolicyRevision: devices.PolicyRevision})
	conversation, turn := origin(t, h, token, "/phone status", device)
	command := n.command(t)
	if command.DeviceId != device || command.Origin.ConversationId != conversation || command.Origin.TurnId != turn || command.GetRead().Kind != v1.ActionKind_ACTION_KIND_READ_STATUS {
		t.Fatal("direct command escaped immutable origin")
	}
	n.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_STARTED)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: command.TaskId, SessionId: command.SessionId, SessionGeneration: command.SessionGeneration, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "EXPLICIT_PRIVATE_STATUS"}}}
	client := v1.NewDeviceServiceClient(h.conn)
	if _, err := client.SyncObservations(tokenContext(token), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	result, err := client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId})
	if err != nil || len(result.Observations) != 1 || result.Observations[0].GetStatus().Model != "EXPLICIT_PRIVATE_STATUS" {
		t.Fatal("direct result unavailable", err)
	}
	assertPrivate(t, h, "EXPLICIT_PRIVATE_STATUS")
}
