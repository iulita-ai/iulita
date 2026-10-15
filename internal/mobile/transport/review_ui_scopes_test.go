package transport

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStoreUIGrantsRequirePhoneScopeAndAllowReduction(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{UI: true, Scenarios: scenarios()})
	token, _ := h.login(t, "owner", "devices:receive", "devices:sync")
	claims, err := h.oauth.ValidateAccess(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	identity := devices.Identity{Token: token, Claims: claims}
	epoch, _, err := h.devices.OpenControl(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range []struct {
		name string
		caps *v1.DeviceCapabilities
	}{
		{"action", &v1.DeviceCapabilities{SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_UI_OBSERVE}}},
		{"category", &v1.DeviceCapabilities{GrantedCategories: []string{"ui_tree"}}},
		{"safety", &v1.DeviceCapabilities{UiSafetyControlAvailable: true}},
	} {
		t.Run(declaration.name, func(t *testing.T) {
			declaration.caps.ProtocolVersion = 1
			declaration.caps.PolicyRevision = devices.PolicyRevision
			declaration.caps.ReportedAt = timestamppb.Now()
			if grantErr := h.devices.Capabilities(context.Background(), identity, epoch, declaration.caps); grantErr == nil {
				t.Fatal("underlying Store accepted UI capabilities without phone:ui")
			}
		})
	}
	uiCaps := &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now(), SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_UI_OBSERVE}, GrantedCategories: []string{"ui_tree"}, UiSafetyControlAvailable: true}
	uiSession := &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, PolicyRevision: devices.PolicyRevision, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), ScreenContentAllowed: true, ApprovedEnvelopeSha256: make([]byte, 32), AllowedActions: uiCaps.SupportedActions}
	// Simulate an existing persisted grant from a more privileged access token.
	// The source token used by these calls is genuinely signed and narrower.
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	capBody, err := proto.Marshal(uiCaps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE mobile_device_streams SET capabilities=? WHERE owner_id=? AND device_id=?`, capBody, claims.Subject, claims.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err = h.devices.Session(context.Background(), identity, epoch, uiSession); err == nil {
		t.Fatal("underlying Store accepted native UI grant without phone:ui")
	}
	sessionBody, err := proto.Marshal(uiSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mobile_native_sessions(owner_id,device_id,id,generation,stream_epoch,family_id,state,metadata,expires_at) VALUES(?,?,?,?,?,?,'active',?,?)`, claims.Subject, claims.DeviceID, uiSession.SessionId, 1, epoch, claims.FamilyID, sessionBody, uiSession.ExpiresAt.AsTime().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	stop := &v1.SessionMirror{SessionId: uiSession.SessionId, SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_REVOKED}
	if err = h.devices.Session(context.Background(), identity, epoch, stop); err != nil {
		t.Fatal("scope reduction prevented native Stop", err)
	}
	if _, err = db.Exec(`UPDATE mobile_native_sessions SET state='active',metadata=?,expires_at=? WHERE id=?`, sessionBody, uiSession.ExpiresAt.AsTime().UnixMilli(), uiSession.SessionId); err != nil {
		t.Fatal(err)
	}
	if err = h.devices.Capabilities(context.Background(), identity, epoch, &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now()}); err != nil {
		t.Fatal("scope reduction prevented capability removal", err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM mobile_native_sessions WHERE id=?`, uiSession.SessionId).Scan(&state); err != nil || state != "revoked" {
		t.Fatal("capability reduction retained UI grant", state, err)
	}
	if err = h.devices.Capabilities(context.Background(), identity, epoch, &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now(), SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, GrantedCategories: []string{"status"}}); err != nil {
		t.Fatal("scope reduction prevented a non-UI declaration", err)
	}
}

func TestStoreNextRequiresUIScopeBeforeDispatchOrAdvancement(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{UI: true, Scenarios: scenarios()})
	source, _ := h.login(t, "owner", "devices:receive", "devices:sync")
	viewer, _ := h.login(t, "owner")
	claims, err := h.oauth.ValidateAccess(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	identity := devices.Identity{Token: source, Claims: claims}
	epoch, _, err := h.devices.OpenControl(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	scenario := scenarios()[1]
	actions := []v1.ActionKind{v1.ActionKind_ACTION_KIND_UI_OBSERVE, v1.ActionKind_ACTION_KIND_UI_CLICK}
	envelope := &v1.TaskEnvelope{Goal: scenario.Goal, Recipient: "fixture.txt", AllowedActions: actions, Scenario: scenario.Binding, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}
	envelopeBody, err := proto.MarshalOptions{Deterministic: true}.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(envelopeBody)
	mirror := &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, PolicyRevision: devices.PolicyRevision, AllowedPackages: []string{scenario.Binding.PackageName}, AllowedActions: actions, ScreenContentAllowed: true, ApprovedEnvelopeSha256: digest[:], ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}
	capabilities := &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now(), SupportedActions: actions, GrantedCategories: []string{"ui_tree"}, UiSafetyControlAvailable: true}
	capBody, err := proto.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	mirrorBody, err := proto.Marshal(mirror)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE mobile_device_streams SET capabilities=? WHERE owner_id=? AND device_id=?`, capBody, claims.Subject, claims.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO mobile_native_sessions(owner_id,device_id,id,generation,stream_epoch,family_id,state,metadata,expires_at) VALUES(?,?,?,?,?,?,'active',?,?)`, claims.Subject, claims.DeviceID, mirror.SessionId, 1, epoch, claims.FamilyID, mirrorBody, mirror.ExpiresAt.AsTime().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	conversation, turn := origin(t, h, viewer, "bounded UI scenario", claims.DeviceID)
	task, err := v1.NewPhoneServiceClient(h.conn).CreateUITask(tokenContext(viewer), &v1.CreateUITaskRequest{Client: clientContext(), Operation: operation(), DeviceId: claims.DeviceID, ConversationId: conversation, TurnId: turn, HistoryGeneration: 1, Envelope: envelope, ApprovedEnvelopeSha256: digest[:]})
	if err != nil {
		t.Fatal(err)
	}
	if command, dispatchErr := h.devices.Next(context.Background(), identity, epoch); dispatchErr == nil || command != nil {
		t.Fatal("underlying Store dispatched UI command without phone:ui", command, dispatchErr)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM mobile_phone_commands WHERE task_id=?`, task.TaskId).Scan(&state); err != nil || state != "queued" {
		t.Fatal("rejected scope changed dispatch evidence", state, err)
	}
	// A missing queued command must not let polling advance a running UI task.
	if _, err = db.Exec(`UPDATE mobile_phone_commands SET state='succeeded' WHERE task_id=?`, task.TaskId); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE mobile_phone_tasks SET state='running' WHERE id=?`, task.TaskId); err != nil {
		t.Fatal(err)
	}
	if command, advanceErr := h.devices.Next(context.Background(), identity, epoch); advanceErr == nil || command != nil {
		t.Fatal("underlying Store admitted UI advancement without phone:ui", command, advanceErr)
	}
}
