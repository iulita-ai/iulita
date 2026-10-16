package transport

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestContinuousTelemetryRotatesTokensAndLeasesButCannotReviveStop(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{})
	initial, device := h.loginCredentials(t, "owner")
	token, refresh := initial.Access, initial.Refresh
	mirror := &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, ExpiresAt: timestamppb.New(time.Now().Add(10 * time.Minute)), PolicyRevision: devices.PolicyRevision}
	n := startNative(t, h, token, mirror)
	claims, err := h.oauth.ValidateAccess(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if device != claims.DeviceID {
		t.Fatal("device binding changed")
	}
	var oldCommands []*v1.Command
	for iteration := 0; iteration < 4; iteration++ {
		_, _ = origin(t, h, token, "/phone status", device)
		cmd := n.command(t)
		n.receipt(t, cmd, v1.ReceiptState_RECEIPT_STATE_STARTED)
		oldCommands = append(oldCommands, cmd)
		reply := h.tokens(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {refresh}})
		token, refresh = reply.Access, reply.Refresh
		mirror = proto.Clone(mirror).(*v1.SessionMirror)
		mirror.SessionGeneration++
		mirror.ExpiresAt = timestamppb.New(time.Now().Add(10 * time.Minute))
		previous := n.epoch
		n = startNative(t, h, token, mirror)
		if n.epoch <= previous {
			t.Fatal("replacement did not fence transport")
		}
		for _, old := range oldCommands {
			task, e := v1.NewDeviceServiceClient(h.conn).GetTask(tokenContext(token), &v1.GetTaskRequest{Client: clientContext(), TaskId: old.TaskId})
			if e != nil || task.State != v1.TaskState_TASK_STATE_UNKNOWN_OUTCOME {
				t.Fatal("old dispatch was replayed or lost uncertainty", e)
			}
		}
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mobile_native_sessions WHERE id=?`, mirror.SessionId).Scan(&count); err != nil || count != 1 {
		t.Fatal("rotation accumulated session records", count, err)
	}
	n.send(t, &v1.ClientControlFrame_Session{Session: &v1.SessionMirror{SessionId: mirror.SessionId, SessionGeneration: mirror.SessionGeneration, State: v1.SessionState_SESSION_STATE_REVOKED}})
	waitNativeMirror(t, h, mirror.SessionId, n.epoch, "revoked")
	claims, err = h.oauth.ValidateAccess(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	identity := devices.Identity{Token: token, Claims: claims}
	epoch, _, err := h.devices.OpenControl(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.devices.Capabilities(context.Background(), identity, epoch, &v1.DeviceCapabilities{ProtocolVersion: 1, SupportedActions: mirror.AllowedActions, GrantedCategories: []string{"status"}, PolicyRevision: devices.PolicyRevision, ReportedAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	mirror.SessionGeneration++
	if err = h.devices.Session(context.Background(), identity, epoch, mirror); err == nil {
		t.Fatal("explicit Stop was revived by renewal")
	}
}
