package transport

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSourceControlLivenessFencesSeparateViewerAndProvider(t *testing.T) {
	for _, fault := range []string{"closed", "control_expired", "source_token_expired"} {
		t.Run(fault, func(t *testing.T) {
			models, provider := newPrivateModels(t)
			h := setupHarnessWithOptions(t, devices.Options{Models: models})
			source, sourceID := h.login(t, "owner")
			viewer, _ := h.login(t, "owner")
			task := startSimulatedTask(t, h, source, sourceID, viewer)
			task.receipt(t, task.started())
			client := v1.NewDeviceServiceClient(h.conn)
			observation := &v1.Observation{ObservationId: uuid.NewString(), TaskId: task.command.TaskId, SessionId: task.session, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "SOURCE_LIVENESS_PRIVATE_CANARY"}}}
			if _, err := client.SyncObservations(tokenContext(source), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{observation}}); err != nil {
				t.Fatal(err)
			}
			ctx := tokenContext(viewer)
			resultRequest := &v1.GetTaskResultRequest{Client: clientContext(), TaskId: task.command.TaskId}
			if _, err := client.GetTaskResult(ctx, resultRequest); err != nil {
				t.Fatal("live source rejected separate viewer", err)
			}
			phone := v1.NewPhoneServiceClient(h.conn)
			prepare := &v1.PrepareProcessingRequest{Client: clientContext(), Operation: operation(), TaskId: task.command.TaskId, ProfileId: "ds-flash", Question: "Describe status", Purpose: v1.ProcessingPurpose_PROCESSING_PURPOSE_SUMMARY}
			offer, err := phone.PrepareProcessing(ctx, prepare)
			if err != nil {
				t.Fatal(err)
			}
			if fault == "closed" {
				if err := h.devices.CloseControl(context.Background(), h.owners["owner"], sourceID, task.epoch); err != nil {
					t.Fatal(err)
				}
			} else {
				db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				query := `UPDATE mobile_device_streams SET expires_at=? WHERE owner_id=? AND device_id=? AND epoch=?`
				if fault == "source_token_expired" {
					query = `UPDATE mobile_device_streams SET token_expires_at=? WHERE owner_id=? AND device_id=? AND epoch=?`
				}
				if _, err := db.Exec(query, time.Now().Add(-time.Second).UnixMilli(), h.owners["owner"], sourceID, task.epoch); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := client.GetTaskResult(ctx, resultRequest); err == nil {
				t.Fatal("inactive source left raw result readable by separate viewer")
			}
			prepare.Operation = operation()
			if _, err := phone.PrepareProcessing(ctx, prepare); err == nil {
				t.Fatal("inactive source allowed a new provider offer")
			}
			if _, err := phone.ProcessResult(ctx, &v1.ProcessResultRequest{Client: clientContext(), Operation: operation(), OfferId: offer.OfferId, ApprovedConsentSha256: offer.ConsentSha256}); err == nil || provider.calls.Load() != 0 {
				t.Fatal("inactive source allowed a provider transfer", err)
			}
			if _, err := h.oauth.ValidateAccess(context.Background(), viewer); err != nil {
				t.Fatal("fault incorrectly revoked the separate viewer", err)
			}
		})
	}
}
