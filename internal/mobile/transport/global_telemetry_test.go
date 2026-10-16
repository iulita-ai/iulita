package transport

import (
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The native mirror is device-wide. Chat ownership and each immutable origin
// remain server checks, and one Stop invalidates results from every chat.
func TestDeviceTelemetrySessionAcrossOwnedChats(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{})
	token, device := h.login(t, "owner")
	foreign, _ := h.login(t, "other")
	mirror := &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)), PolicyRevision: devices.PolicyRevision}
	n := startNative(t, h, token, mirror)
	client := v1.NewDeviceServiceClient(h.conn)
	var commands []*v1.Command
	var conversations []string
	for i := 0; i < 2; i++ {
		conversation, turn := origin(t, h, token, "/phone status", device)
		command := n.command(t)
		if command.SessionId != mirror.SessionId || command.SessionGeneration != 1 || command.Origin.ConversationId != conversation || command.Origin.TurnId != turn || command.DeviceId != device {
			t.Fatal("shared session escaped immutable task authority")
		}
		n.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_STARTED)
		observation := &v1.Observation{ObservationId: uuid.NewString(), TaskId: command.TaskId, SessionId: command.SessionId, SessionGeneration: command.SessionGeneration, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "GLOBAL_TELEMETRY_PRIVATE_CANARY"}}}
		if _, err := client.SyncObservations(tokenContext(token), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{observation}}); err != nil {
			t.Fatal(err)
		}
		n.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_SUCCEEDED)
		result, err := client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId})
		if err != nil || len(result.Observations) != 1 {
			t.Fatal("shared session result missing", err)
		}
		if _, err := client.GetTaskResult(tokenContext(foreign), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId}); err == nil {
			t.Fatal("global device grant exposed result to another owner")
		}
		if _, err := v1.NewChatServiceClient(h.conn).SendMessage(tokenContext(foreign), &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "/phone status", TargetDeviceId: device}); err == nil {
			t.Fatal("global device grant admitted a foreign chat writer")
		}
		commands = append(commands, command)
		conversations = append(conversations, conversation)
	}
	if conversations[0] == conversations[1] {
		t.Fatal("test did not exercise distinct chats")
	}
	n.send(t, &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: mirror.SessionId, SessionGeneration: 2, State: v1.SessionState_SESSION_STATE_REVOKED, PolicyRevision: devices.PolicyRevision}})
	waitNativeMirror(t, h, mirror.SessionId, n.epoch, "revoked")
	for _, command := range commands {
		if _, err := client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId}); err == nil {
			t.Fatal("Stop left a chat result accessible")
		}
	}
	assertPrivate(t, h, "GLOBAL_TELEMETRY_PRIVATE_CANARY")
}
