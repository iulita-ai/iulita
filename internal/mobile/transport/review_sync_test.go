package transport

import (
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSyncOperationRejectsChangedPayload(t *testing.T) {
	h := setupHarness(t)
	token, device := h.login(t, "owner")
	task := startSimulatedTask(t, h, token, device, token)
	task.receipt(t, task.started())
	client := v1.NewDeviceServiceClient(h.conn)
	o := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "first"}}}
	r := &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{o}}
	if _, err := client.SyncObservations(tokenContext(token), r); err != nil {
		t.Fatal(err)
	}
	if ack, err := client.SyncObservations(tokenContext(token), r); err != nil || len(ack.AcceptedObservationIds) != 1 || ack.AcceptedObservationIds[0] != o.ObservationId {
		t.Fatal("lost ACK replay changed result", ack, err)
	}
	r2 := proto.Clone(r).(*v1.SyncObservationsRequest)
	r2.Observations[0].ObservationId = uuid.NewString()
	r2.Observations[0].GetStatus().Model = "changed"
	if _, err := client.SyncObservations(tokenContext(token), r2); status.Code(err) != codes.AlreadyExists {
		t.Fatal("same operation ID with changed semantic payload was accepted")
	}
}

func TestSyncNewOperationBudgetDoesNotBlockExactRetry(t *testing.T) {
	h := setupHarness(t)
	token, deviceID := h.login(t, "owner")
	task := startSimulatedTask(t, h, token, deviceID, token)
	task.receipt(t, task.started())
	client := v1.NewDeviceServiceClient(h.conn)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "once"}}}
	request := &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}
	var first *v1.SyncObservationsRequest
	for i := 0; i < 64; i++ {
		request.Operation = operation()
		if i == 0 {
			first = proto.Clone(request).(*v1.SyncObservationsRequest)
		}
		if _, err := client.SyncObservations(tokenContext(token), request); err != nil {
			t.Fatal("operation budget exhausted early", i, err)
		}
	}
	request.Operation = operation()
	if _, err := client.SyncObservations(tokenContext(token), request); status.Code(err) != codes.ResourceExhausted {
		t.Fatal("new UUID bypassed task operation budget", err)
	}
	if _, err := client.SyncObservations(tokenContext(token), first); err != nil {
		t.Fatal("quota broke a lost ACK retry", err)
	}
}
