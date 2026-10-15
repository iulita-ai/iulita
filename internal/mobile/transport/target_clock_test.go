package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/serverclock"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestExplicitPhoneTargetCannotBeChangedByModelOrReplay(t *testing.T) {
	h := setupHarness(t)
	token, selected := h.login(t, "owner")
	_, otherOwned := h.login(t, "owner")
	_, foreign := h.login(t, "other")
	conversation, _ := origin(t, h, token, "plain chat")
	chat := v1.NewChatServiceClient(h.conn)
	// The provider deliberately requests the other owned device. Ownership is
	// insufficient: the selected turn target must prevent phone-task admission.
	in := &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: conversation, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: "phone-status:" + otherOwned, TargetDeviceId: selected}
	turn, err := chat.SendMessage(tokenContext(token), in)
	if err != nil || turn.TargetDeviceId != selected {
		t.Fatal("selected target not admitted", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, lookupErr := chat.GetTurn(tokenContext(token), &v1.GetTurnRequest{Client: clientContext(), ConversationId: conversation, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if got.State == v1.TurnState_TURN_STATE_COMPLETED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wrong-target tool did not terminate safely")
		}
		time.Sleep(10 * time.Millisecond)
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mobile_phone_tasks WHERE turn_id=?`, turn.TurnId).Scan(&count); err != nil || count != 0 {
		t.Fatal("model bypassed selected target", count, err)
	}
	in.Operation = operation()
	in.TargetDeviceId = otherOwned
	if _, err = chat.SendMessage(tokenContext(token), in); status.Code(err) != codes.AlreadyExists {
		t.Fatal("same message retargeted", err)
	}
	in.Operation, in.ClientMessageId, in.TargetDeviceId = operation(), uuid.NewString(), foreign
	if _, err = chat.SendMessage(tokenContext(token), in); status.Code(err) != codes.NotFound {
		t.Fatal("foreign target admitted", err)
	}
}

func TestRuntimeTimeSampleAndNativeSessionEpochAgree(t *testing.T) {
	clock := serverclock.New()
	h := setupHarnessWithOptions(t, devices.Options{Clock: clock})
	token, _ := h.login(t, "owner")
	profile, err := v1.NewAccountServiceClient(h.conn).GetProfile(tokenContext(token), &v1.GetProfileRequest{Client: clientContext()})
	if err != nil || profile.Server.ClockId != clock.ID() {
		t.Fatal("gRPC clock mismatch", err)
	}
	nonce, err := oauthstore.Secret()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", harnessIssuer+"/oauth2/time?nonce="+nonce, http.NoBody)
	w := httptest.NewRecorder()
	h.browser.ServeHTTP(w, r)
	var sample struct {
		ClockID string `json:"clock_id"`
		Nonce   string `json:"nonce"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &sample) != nil || sample.ClockID != profile.Server.ClockId || sample.Nonce != nonce {
		t.Fatal("OAuth clock mismatch", w.Code)
	}
	claims, err := h.oauth.ValidateAccess(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	identity := devices.Identity{Token: token, Claims: claims}
	epoch, _, err := h.devices.OpenControl(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	caps := &v1.DeviceCapabilities{ProtocolVersion: 1, PolicyRevision: devices.PolicyRevision, SupportedActions: []v1.ActionKind{v1.ActionKind_ACTION_KIND_READ_STATUS}, GrantedCategories: []string{"status"}, ReportedAt: timestamppb.Now()}
	if err = h.devices.Capabilities(context.Background(), identity, epoch, caps); err != nil {
		t.Fatal(err)
	}
	mirror := &v1.SessionMirror{SessionId: uuid.NewString(), SessionGeneration: 1, State: v1.SessionState_SESSION_STATE_ACTIVE, AllowedActions: caps.SupportedActions, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)), PolicyRevision: devices.PolicyRevision, ClockId: uuid.NewString()}
	if err = h.devices.Session(context.Background(), identity, epoch, mirror); err == nil {
		t.Fatal("wrong clock epoch accepted")
	}
	mirror.ClockId = sample.ClockID
	if err = h.devices.Session(context.Background(), identity, epoch, mirror); err != nil {
		t.Fatal("fresh sample did not bind session", err)
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("clock endpoint leaked credential")
	}
}
