package transport

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type simulatedTask struct {
	stream  grpc.BidiStreamingClient[v1.ClientControlFrame, v1.ServerControlFrame]
	command *v1.Command
	epoch   uint64
	session string
}

func startSimulatedTask(t *testing.T, h *harness, source, deviceID, viewer string) simulatedTask {
	t.Helper()
	ctx, cancel := context.WithTimeout(tokenContext(source), 8*time.Second)
	t.Cleanup(cancel)
	client := v1.NewDeviceServiceClient(h.conn)
	stream, err := client.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, Body: &v1.ClientControlFrame_Hello{Hello: &v1.ControlHello{Client: clientContext()}}}); err != nil {
		t.Fatal(err)
	}
	welcome, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	epoch := welcome.ControlStreamEpoch
	session := uuid.NewString()
	frames := []*v1.ClientControlFrame{
		{Body: &v1.ClientControlFrame_Capabilities{Capabilities: &v1.DeviceCapabilities{ProtocolVersion: 1, SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, GrantedCategories: []string{"status"}, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now()}}},
		{Body: &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: session, SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), PolicyRevision: devices.PolicyRevision}}},
	}
	for _, frame := range frames {
		frame.ProtocolVersion = 1
		frame.ControlStreamEpoch = epoch
		if err = stream.Send(frame); err != nil {
			t.Fatal(err)
		}
	}
	// Observe committed metadata instead of guessing when the native reports won.
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		err = db.QueryRow(`SELECT COUNT(*) FROM mobile_native_sessions WHERE id=? AND state='active'`, session).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native report not committed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	chat := v1.NewChatServiceClient(h.conn)
	c, err := chat.CreateConversation(tokenContext(viewer), &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "simulated phone", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = chat.SendMessage(tokenContext(viewer), &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "phone-status:" + deviceID}); err != nil {
		t.Fatal(err)
	}
	frame, err := stream.Recv()
	if err != nil || frame.GetCommand() == nil {
		t.Fatal("command", frame, err)
	}
	return simulatedTask{stream: stream, command: frame.GetCommand(), epoch: epoch, session: session}
}
func (s simulatedTask) receipt(t *testing.T, receipt *v1.CommandReceipt) {
	t.Helper()
	if err := s.stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: s.epoch, Body: &v1.ClientControlFrame_Receipt{Receipt: receipt}}); err != nil {
		t.Fatal(err)
	}
	ack, err := s.stream.Recv()
	if err != nil || ack.GetReceiptAck() == nil || ack.GetReceiptAck().ReceiptId != receipt.ReceiptId {
		t.Fatal("receipt acknowledgement", ack, err)
	}
}
func (s simulatedTask) started() *v1.CommandReceipt {
	return &v1.CommandReceipt{ReceiptId: uuid.NewString(), CommandId: s.command.CommandId, TaskId: s.command.TaskId, ServerSeq: s.command.ServerSeq, State: v1.ReceiptState_RECEIPT_STATE_STARTED, SessionGeneration: 1, OccurredAt: timestamppb.Now(), DispatchStarted: true, PayloadDigestSha256: s.command.PayloadDigestSha256}
}

func TestPhoneViewerBindingAndImmediateSourceRevocation(t *testing.T) {
	h := setupHarness(t)
	source, sourceID := h.login(t, "owner")
	viewer, _ := h.login(t, "owner")
	foreign, _ := h.login(t, "other")
	task := startSimulatedTask(t, h, source, sourceID, viewer)
	receipt := task.started()
	task.receipt(t, receipt)
	task.receipt(t, receipt) // lost ACK exact retry
	client := v1.NewDeviceServiceClient(h.conn)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "temporary"}}}
	if _, err := client.SyncObservations(tokenContext(source), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	req := &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId}
	if _, err := client.GetTaskResult(tokenContext(viewer), req); err != nil {
		t.Fatal("explicit viewer denied", err)
	}
	for _, token := range []string{source, foreign} {
		if _, err := client.GetTaskResult(tokenContext(token), req); status.Code(err) != codes.NotFound {
			t.Fatal("foreign result viewer", err)
		}
	}
	// The main account reservation + OAuth transaction must invalidate the source
	// immediately, while the distinct viewer's own access token remains valid.
	if err := h.oauth.RevokeDevice(context.Background(), h.owners["owner"], sourceID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetTaskResult(tokenContext(viewer), req); status.Code(err) != codes.NotFound {
		t.Fatal("source revoke left body readable", err)
	}
	got, err := client.GetTask(tokenContext(viewer), &v1.GetTaskRequest{Client: clientContext(), TaskId: task.command.TaskId})
	if err != nil || got.State != v1.TaskState_TASK_STATE_UNKNOWN_OUTCOME || !got.CancellationRequested {
		t.Fatal("revocation fabricated no effect", got, err)
	}
}
func TestControlReconnectFencesOldStreamAndNeverRequeuesDispatchedCommand(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner")
	task := startSimulatedTask(t, h, token, deviceID, token)
	task.receipt(t, task.started())
	ctx, cancel := context.WithTimeout(tokenContext(token), time.Second)
	defer cancel()
	replacement, err := v1.NewDeviceServiceClient(h.conn).Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = replacement.Send(&v1.ClientControlFrame{ProtocolVersion: 1, Body: &v1.ClientControlFrame_Hello{Hello: &v1.ControlHello{Client: clientContext()}}}); err != nil {
		t.Fatal(err)
	}
	welcome, err := replacement.Recv()
	if err != nil || welcome.ControlStreamEpoch <= task.epoch {
		t.Fatal("epoch not fenced", welcome, err)
	}
	if _, err = task.stream.Recv(); err == nil {
		t.Fatal("old stream retained authority")
	}
	if frame, stepErr1 := replacement.Recv(); stepErr1 == nil || frame.GetCommand() != nil {
		t.Fatal("dispatched command replayed", frame, stepErr1)
	}
	got, err := v1.NewDeviceServiceClient(h.conn).GetTask(tokenContext(token), &v1.GetTaskRequest{Client: clientContext(), TaskId: task.command.TaskId})
	if err != nil || got.State != v1.TaskState_TASK_STATE_UNKNOWN_OUTCOME {
		t.Fatal("reconnect lost uncertain outcome", got, err)
	}
}
func TestChatOnlyAuthorityCannotCreatePhoneWork(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner", "chats:read", "chats:write")
	client := v1.NewChatServiceClient(h.conn)
	ctx := tokenContext(token)
	c, err := client.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "no phone scopes", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := client.SendMessage(ctx, &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "phone-status:" + deviceID})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, stepErr2 := client.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: c.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
		if stepErr2 != nil {
			t.Fatal(stepErr2)
		}
		if got.State == v1.TurnState_TURN_STATE_FAILED || got.State == v1.TurnState_TURN_STATE_COMPLETED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unadvertised tool did not fail closed", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mobile_phone_tasks`).Scan(&n); err != nil || n != 0 {
		t.Fatal("chat-only token dispatched phone work", n, err)
	}
}

func TestNativeStopImmediatelyDeniesResultsAndCannotReviveSameGeneration(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner")
	task := startSimulatedTask(t, h, token, deviceID, token)
	task.receipt(t, task.started())
	client := v1.NewDeviceServiceClient(h.conn)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "transient"}}}
	if _, err := client.SyncObservations(tokenContext(token), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	if err := task.stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: task.epoch, Body: &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: task.session, SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_REVOKED}}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		_, err := client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId})
		if status.Code(err) == codes.NotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native stop left result readable", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := task.stream.Send(&v1.ClientControlFrame{ProtocolVersion: 1, ControlStreamEpoch: task.epoch, Body: &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: task.session, SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), PolicyRevision: devices.PolicyRevision}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := task.stream.Recv(); err == nil {
		t.Fatal("native session revived after Stop without new generation")
	}
}

func TestCompletedTurnCancellationRevokesAsynchronousPhoneTask(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner")
	task := startSimulatedTask(t, h, token, deviceID, token)
	task.receipt(t, task.started())
	chat := v1.NewChatServiceClient(h.conn)
	ctx := tokenContext(token)
	lookup := &v1.GetTurnRequest{Client: clientContext(), ConversationId: task.command.Origin.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: task.command.Origin.TurnId}}
	deadline := time.Now().Add(2 * time.Second)
	for {
		turn, err := chat.GetTurn(ctx, lookup)
		if err != nil {
			t.Fatal(err)
		}
		if turn.State == v1.TurnState_TURN_STATE_COMPLETED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("phone parent did not complete", turn)
		}
		time.Sleep(5 * time.Millisecond)
	}
	turn, err := chat.CancelTurn(ctx, &v1.CancelTurnRequest{Client: clientContext(), Operation: operation(), ConversationId: lookup.ConversationId, TurnId: task.command.Origin.TurnId, HistoryGeneration: 1})
	if err != nil || turn.State != v1.TurnState_TURN_STATE_COMPLETED || !turn.CancellationRequested {
		t.Fatal("terminal result lost or async authority retained", turn, err)
	}
	revoked, err := task.stream.Recv()
	if err != nil || revoked.GetRevoked() == nil {
		t.Fatal("native revocation not sent", revoked, err)
	}
	got, err := v1.NewDeviceServiceClient(h.conn).GetTask(ctx, &v1.GetTaskRequest{Client: clientContext(), TaskId: task.command.TaskId})
	if err != nil || !got.CancellationRequested || got.State != v1.TaskState_TASK_STATE_UNKNOWN_OUTCOME {
		t.Fatal("canceled parent left asynchronous task live", got, err)
	}
}
