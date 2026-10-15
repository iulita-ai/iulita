package transport

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSourceFamilyRevocationDeniesSeparateViewer(t *testing.T) {
	h := setupHarness(t)
	source, sourceID := h.login(t, "owner")
	viewer, _ := h.login(t, "owner")
	task := startSimulatedTask(t, h, source, sourceID, viewer)
	task.receipt(t, task.started())
	client := v1.NewDeviceServiceClient(h.conn)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "SECOPS_CANARY"}}}
	if _, err := client.SyncObservations(tokenContext(source), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	claims, err := h.oauth.ValidateAccess(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.oauth.RevokeFamily(context.Background(), claims.Subject, claims.FamilyID); err != nil {
		t.Fatal(err)
	}
	_, streamErr := task.stream.Recv()
	if streamErr == nil {
		t.Fatal("source control not revoked")
	}
	_, err = client.GetTaskResult(tokenContext(viewer), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId})
	if status.Code(err) != codes.NotFound {
		t.Fatal("source family revoked and Control closed, but other viewer still reads source canary")
	}
}

func TestCapabilityReductionRevokesExistingResult(t *testing.T) {
	h := setupHarness(t)
	source, sourceID := h.login(t, "owner")
	viewer, _ := h.login(t, "owner")
	task := startSimulatedTask(t, h, source, sourceID, viewer)
	task.receipt(t, task.started())
	client := v1.NewDeviceServiceClient(h.conn)
	obs := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "SECOPS_CANARY"}}}
	if _, err := client.SyncObservations(tokenContext(source), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{obs}}); err != nil {
		t.Fatal(err)
	}
	claims, err := h.oauth.ValidateAccess(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.devices.Capabilities(context.Background(), devices.Identity{Token: source, Claims: claims}, task.epoch, &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	_, err = client.GetTaskResult(tokenContext(viewer), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId})
	if status.Code(err) != codes.NotFound {
		t.Fatal("source removed action/category, but existing result still readable")
	}
	if err = h.devices.Capabilities(context.Background(), devices.Identity{Token: source, Claims: claims}, task.epoch, &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now(), SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, GrantedCategories: []string{"status"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetTaskResult(tokenContext(viewer), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId}); status.Code(err) != codes.NotFound {
		t.Fatal("capability regrant revived old result", err)
	}

}

func TestOAuthCleanupRetainsControlFamilyFence(t *testing.T) {
	h := setupHarness(t)
	source, _ := h.login(t, "owner")
	claims, err := h.oauth.ValidateAccess(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.devices.OpenControl(context.Background(), devices.Identity{Token: source, Claims: claims}); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE oauth_families SET expires_at=? WHERE id=?`, time.Now().Add(-8*24*time.Hour).Unix(), claims.FamilyID); err != nil {
		t.Fatal(err)
	}
	if err = h.oauth.Sweep(context.Background()); err != nil {
		t.Fatalf("maintenance deleted a referenced authority family: %v", err)
	}
}
