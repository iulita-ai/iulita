package transport

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSimulatedDeviceControlReceiptPrivacyAndClear(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner")
	ctx, cancel := context.WithTimeout(tokenContext(token), 8*time.Second)
	defer cancel()
	device := v1.NewDeviceServiceClient(h.conn)
	stream, err := device.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, Body: &v1.ClientControlFrame_Hello{Hello: &v1.ControlHello{Client: clientContext()}}}); err != nil {
		t.Fatal(err)
	}
	welcome, err := stream.Recv()
	if err != nil || welcome.GetWelcome() == nil {
		t.Fatal("control welcome", err)
	}
	epoch := welcome.ControlStreamEpoch
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: epoch, Body: &v1.ClientControlFrame_Capabilities{Capabilities: &v1.DeviceCapabilities{ProtocolVersion: 1, SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, GrantedCategories: []string{"status"}, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now()}}}); err != nil {
		t.Fatal(err)
	}
	sessionID := uuid.NewString()
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: epoch, Body: &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: sessionID, SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)), PolicyRevision: devices.PolicyRevision}}}); err != nil {
		t.Fatal(err)
	}
	// Wait for the two ordered native reports to commit before requesting work.
	time.Sleep(50 * time.Millisecond)
	chat := v1.NewChatServiceClient(h.conn)
	conversation, err := chat.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "phone", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chat.SendMessage(ctx, &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "phone-status:" + deviceID})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Recv()
	if err != nil || frame.GetCommand() == nil {
		t.Fatal("phone command", err, frame)
	}
	command := frame.GetCommand()
	if command.DeviceId != deviceID || command.Origin.ConversationId != conversation.ConversationId || command.SessionId != sessionID {
		t.Fatal("command binding")
	}
	receipt := &v1.CommandReceipt{ReceiptId: uuid.NewString(), CommandId: command.CommandId, TaskId: command.TaskId, ServerSeq: command.ServerSeq, State: v1.ReceiptState_RECEIPT_STATE_STARTED, SessionGeneration: 1, OccurredAt: timestamppb.Now(), DispatchStarted: true, PayloadDigestSha256: command.PayloadDigestSha256}
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: epoch, Body: &v1.ClientControlFrame_Receipt{Receipt: receipt}}); err != nil {
		t.Fatal(err)
	}
	ack, err := stream.Recv()
	if err != nil || ack.GetReceiptAck() == nil || ack.GetReceiptAck().ReceiptId != receipt.ReceiptId {
		t.Fatal("receipt ack", err)
	}
	const canary = "PRIVATE_PHONE_CANARY_NEVER_DURABLE"
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: command.TaskId, SessionId: sessionID, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: canary, OsVersion: "synthetic"}}}
	if _, err = device.SyncObservations(ctx, &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal("observation sync", err)
	}
	result, err := device.GetTaskResult(ctx, &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId})
	if err != nil || len(result.Observations) != 1 || result.Observations[0].GetStatus().Model != canary {
		t.Fatal("scoped ephemeral result", err)
	}
	receipt.ReceiptId = uuid.NewString()
	receipt.State = v1.ReceiptState_RECEIPT_STATE_SUCCEEDED
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: epoch, Body: &v1.ClientControlFrame_Receipt{Receipt: receipt}}); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	task, err := device.GetTask(ctx, &v1.GetTaskRequest{Client: clientContext(), TaskId: command.TaskId})
	if err != nil || task.State != v1.TaskState_TASK_STATE_SUCCEEDED {
		t.Fatal("truthful task outcome", task, err)
	}
	messages, err := chat.ListMessages(ctx, &v1.ListMessagesRequest{Client: clientContext(), ConversationId: conversation.ConversationId, HistoryGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(messages)
	if strings.Contains(string(serialized), canary) {
		t.Fatal("raw phone content entered history")
	}
	for _, path := range h.paths {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			body, stepErr1 := os.ReadFile(path + suffix)
			if stepErr1 == nil && strings.Contains(string(body), canary) {
				t.Fatal("phone canary in DB/WAL", path+suffix)
			}
		}
	}
	if _, err = chat.ClearHistory(ctx, &v1.ClearHistoryRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation.ConversationId, ExpectedHistoryGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = device.GetTaskResult(ctx, &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId}); status.Code(err) != codes.NotFound {
		t.Fatal("clear resurrected phone result", err)
	}
}
