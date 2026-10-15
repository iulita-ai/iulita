package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReviewFrozenSnapshotMustRemainCurrent(t *testing.T) {
	scenario := scenarios()[1]
	scenario.Reasoning = true
	m, provider := newPrivateModels(t)
	h := setupHarnessWithOptions(t, devices.Options{Models: m, UI: true, Scenarios: []devices.Scenario{scenario}})
	token, device := h.login(t, "owner")
	native, task, envelope := createUI(t, h, token, device, scenario, false)
	uiSessions.Store(task.TaskId, native.session)
	defer uiSessions.Delete(task.TaskId)
	treeA := uiTree(scenario)
	treeA.Nodes = []*v1.UINode{node("file.details", "Button", v1.ActionKind_ACTION_KIND_UI_CLICK, "A"), node("file.name", "TextView", v1.ActionKind_ACTION_KIND_UI_OBSERVE, envelope.Recipient)}
	pushTree(t, h, token, task, treeA)
	treeB := uiTree(scenario)
	treeB.Nodes = []*v1.UINode{node("file.details", "Button", v1.ActionKind_ACTION_KIND_UI_CLICK, "B"), node("file.name", "TextView", v1.ActionKind_ACTION_KIND_UI_OBSERVE, envelope.Recipient)}
	provider.complete = func(context.Context, llm.Request) (llm.Response, error) {
		pushTree(t, h, token, task, treeB)
		body, _ := json.Marshal(map[string]any{"snapshot_id": treeA.SnapshotId, "step": 0})
		return llm.Response{Content: string(body)}, nil
	}
	phone := v1.NewPhoneServiceClient(h.conn)
	offer, err := phone.PrepareProcessing(tokenContext(token), &v1.PrepareProcessingRequest{Client: clientContext(), Operation: operation(), TaskId: task.TaskId, ProfileId: "ds-flash", Purpose: v1.ProcessingPurpose_PROCESSING_PURPOSE_UI_PLAN})
	if err != nil {
		t.Fatal(err)
	}
	_, err = phone.ProcessResult(tokenContext(token), &v1.ProcessResultRequest{Client: clientContext(), Operation: operation(), OfferId: offer.OfferId, ApprovedConsentSha256: offer.ConsentSha256})
	if err == nil {
		command := native.command(t)
		t.Fatalf("accepted frozen snapshot A %s but dispatched B %s node=%s", treeA.SnapshotId, command.SnapshotId, command.GetUi().NodeHandle)
	}
}

func TestPreparedConsentPreviewIsExactAndImmutable(t *testing.T) {
	models, provider := newPrivateModels(t)
	h := setupHarnessWithOptions(t, devices.Options{Models: models})
	token, device := h.login(t, "owner")
	_, command := statusTask(t, h, token, device)
	phone := v1.NewPhoneServiceClient(h.conn)
	prepare := &v1.PrepareProcessingRequest{Client: clientContext(), Operation: operation(), TaskId: command.TaskId, ProfileId: "ds-flash", Question: "PRIVATE_PREVIEW_QUESTION_CANARY", Purpose: v1.ProcessingPurpose_PROCESSING_PURPOSE_SUMMARY}
	offer, err := phone.PrepareProcessing(tokenContext(token), prepare)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(offer.InputPreviewJson, []byte("PRIVATE_RAW_COMPLETION_CANARY")) || !bytes.Equal(offer.InputSha256, sha256Sum(offer.InputPreviewJson)) || offer.Question != prepare.Question {
		t.Fatal("exact preview not disclosed")
	}
	// Data can change after an earlier display. The offered preview remains A,
	// and approval of A never silently authorizes the later observation B.
	observation := &v1.Observation{ObservationId: uuid.NewString(), TaskId: command.TaskId, SessionId: command.SessionId, SessionGeneration: 1, Category: v1.ObservationCategory_OBSERVATION_CATEGORY_STATUS, CollectedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), Value: &v1.Observation_Status{Status: &v1.DeviceStatus{Model: "PRIVATE_PREVIEW_LATER_CANARY"}}}
	if _, syncErr := v1.NewDeviceServiceClient(h.conn).SyncObservations(tokenContext(token), &v1.SyncObservationsRequest{Client: clientContext(), Operation: operation(), Observations: []*v1.Observation{observation}}); syncErr != nil {
		t.Fatal(syncErr)
	}
	replay, err := phone.PrepareProcessing(tokenContext(token), prepare)
	if err != nil || !proto.Equal(replay, offer) {
		t.Fatal("prepared preview changed on replay", err)
	}
	if _, processErr := phone.ProcessResult(tokenContext(token), &v1.ProcessResultRequest{Client: clientContext(), Operation: operation(), OfferId: offer.OfferId, ApprovedConsentSha256: offer.ConsentSha256}); processErr != nil {
		t.Fatal(processErr)
	}
	captured := <-provider.received
	if captured.Message != offer.Question+"\nUNTRUSTED_PHONE_DATA_JSON:\n"+string(offer.InputPreviewJson) || strings.Contains(captured.Message, "PRIVATE_PREVIEW_LATER_CANARY") {
		t.Fatal("transferred input differs from approved preview")
	}
	prepare.Operation = operation()
	later, err := phone.PrepareProcessing(tokenContext(token), prepare)
	if err != nil || !bytes.Contains(later.InputPreviewJson, []byte("PRIVATE_PREVIEW_LATER_CANARY")) || bytes.Equal(later.ConsentSha256, offer.ConsentSha256) {
		t.Fatal("new consent does not disclose changed input", err)
	}
	// Lost-ACK replay keeps its original immutable offer even after model reload.
	models.current.Store(nil)
	if retained, replayErr := phone.PrepareProcessing(tokenContext(token), prepare); replayErr != nil || !proto.Equal(retained, later) {
		t.Fatal("model reload destroyed immutable prepare replay", replayErr)
	}
	assertPrivate(t, h, "PRIVATE_PREVIEW_QUESTION_CANARY", "PRIVATE_PREVIEW_LATER_CANARY", "PRIVATE_RAW_COMPLETION_CANARY")
}

func sha256Sum(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }
