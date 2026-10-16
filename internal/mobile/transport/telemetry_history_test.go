package transport

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/ephemeral"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeferredTelemetryHistoryRetryPrivacyRehydrationAndRevocation(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{})
	creds, device := h.loginCredentials(t, "owner")
	other, _ := h.login(t, "other")
	otherDevice, _ := h.login(t, "owner")
	client := v1.NewDeviceTelemetryServiceClient(h.conn)
	captured := time.Now().Add(-16 * time.Hour)
	collection := uuid.NewString()
	first := &v1.TelemetryRecord{RecordId: uuid.NewString(), CollectionId: collection, CapturedAt: timestamppb.New(captured), Value: &v1.TelemetryRecord_Status{Status: &v1.DeviceStatus{Model: "HISTORY_PRIVATE_CANARY", NetworkType: "wifi"}}}
	second := &v1.TelemetryRecord{RecordId: uuid.NewString(), CollectionId: collection, CapturedAt: timestamppb.New(captured.Add(time.Minute)), Value: &v1.TelemetryRecord_Location{Location: &v1.LocationReading{Latitude: 55.75, Longitude: 37.61, AccuracyMeters: 10, Provider: "synthetic"}}}
	request := &v1.UploadTelemetryRequest{Client: clientContext(), Records: []*v1.TelemetryRecord{first, second}}
	for i := 0; i < 2; i++ {
		reply, err := client.UploadBatch(tokenContext(creds.Access), request)
		if err != nil || len(reply.AcceptedRecordIds) != 2 {
			t.Fatal("offline upload/lost ACK retry failed", err)
		}
	}
	list, err := client.ListHistory(tokenContext(creds.Access), &v1.ListTelemetryRequest{Client: clientContext(), PageSize: 1})
	if err != nil || len(list.Records) != 1 || list.Records[0].RecordId != second.RecordId || list.NextRecordId == "" {
		t.Fatal("history ordering/page failed", err)
	}
	next, err := client.ListHistory(tokenContext(creds.Access), &v1.ListTelemetryRequest{Client: clientContext(), BeforeRecordId: list.NextRecordId, PageSize: 1})
	if err != nil || len(next.Records) != 1 || !proto.Equal(next.Records[0], first) {
		t.Fatal("history was retimestamped or omitted", err)
	}
	for _, token := range []string{other, otherDevice} {
		foreign, e := client.ListHistory(tokenContext(token), &v1.ListTelemetryRequest{Client: clientContext()})
		if e != nil || len(foreign.Records) != 0 {
			t.Fatal("history escaped token-device ownership", e)
		}
		if _, e = client.ListHistory(tokenContext(token), &v1.ListTelemetryRequest{Client: clientContext(), BeforeRecordId: first.RecordId}); e == nil {
			t.Fatal("foreign cursor was accepted")
		}
	}
	changed := proto.Clone(first).(*v1.TelemetryRecord)
	changed.GetStatus().Model = "DIFFERENT_PRIVATE_CANARY"
	if _, err = client.UploadBatch(tokenContext(creds.Access), &v1.UploadTelemetryRequest{Client: clientContext(), Records: []*v1.TelemetryRecord{changed}}); status.Code(err) != codes.AlreadyExists {
		t.Fatal("changed duplicate was accepted", err)
	}
	stale := proto.Clone(first).(*v1.TelemetryRecord)
	stale.RecordId = uuid.NewString()
	stale.CapturedAt = timestamppb.New(time.Now().Add(-25 * time.Hour))
	fresh := proto.Clone(first).(*v1.TelemetryRecord)
	fresh.RecordId = uuid.NewString()
	if _, err = client.UploadBatch(tokenContext(creds.Access), &v1.UploadTelemetryRequest{Client: clientContext(), Records: []*v1.TelemetryRecord{fresh, stale}}); err == nil {
		t.Fatal("expired capture accepted")
	}
	db, err := sqlitedb.Open(context.Background(), h.paths[1], false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var expiry int64
	if err = db.QueryRow(`SELECT COUNT(*) FROM mobile_telemetry_records`).Scan(&count); err != nil || count != 2 {
		t.Fatal("batch was not atomic or retry duplicated rows", count, err)
	}
	if err = db.QueryRow(`SELECT expires_at FROM mobile_telemetry_records WHERE id=?`, first.RecordId).Scan(&expiry); err != nil || expiry != captured.Add(24*time.Hour).UnixMilli() {
		t.Fatal("upload reset capture TTL", expiry, err)
	}
	// A fresh encrypted RAM store simulates process history loss. Metadata alone
	// yields no body; retry of the client's retained immutable record rehydrates it.
	content, _ := ephemeral.New()
	reopened, err := devices.Open(context.Background(), h.paths[1], h.oauth, h.server.config.Accounts, h.chats, content)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	claims, err := h.oauth.ValidateAccess(context.Background(), creds.Access)
	if err != nil {
		t.Fatal(err)
	}
	identity := devices.Identity{Token: creds.Access, Claims: claims}
	empty, _, err := reopened.ListTelemetry(context.Background(), identity, time.Now().Add(-24*time.Hour), "", 100)
	if err != nil || len(empty) != 0 {
		t.Fatal("metadata reconstructed private history", err)
	}
	if _, err = reopened.UploadTelemetry(context.Background(), identity, request.Records); err != nil {
		t.Fatal("reupload after RAM loss failed", err)
	}
	restored, _, err := reopened.ListTelemetry(context.Background(), identity, time.Now().Add(-24*time.Hour), "", 100)
	if err != nil || len(restored) != 2 {
		t.Fatal("retained client copy could not restore history", err)
	}
	assertPrivate(t, h, "HISTORY_PRIVATE_CANARY")
	assertPrivate(t, h, "DIFFERENT_PRIVATE_CANARY")
	rotated := h.tokens(t, url.Values{"grant_type": {"refresh_token"}, "client_id": {"mobile"}, "refresh_token": {creds.Refresh}})
	if _, err = client.UploadBatch(tokenContext(rotated.Access), request); err != nil {
		t.Fatal("token rotation broke deferred history", err)
	}
	revoked := h.http("/oauth2/revoke", url.Values{"client_id": {"mobile"}, "token": {rotated.Refresh}, "token_type_hint": {"refresh_token"}}, nil)
	if revoked.Code != 200 {
		t.Fatal("fixture revoke failed")
	}
	if _, err = client.UploadBatch(tokenContext(rotated.Access), request); err == nil {
		t.Fatal("revoked writer retained history authority")
	}
	if _, err = client.ListHistory(tokenContext(rotated.Access), &v1.ListTelemetryRequest{Client: clientContext()}); err == nil {
		t.Fatal("revoked reader retained history authority")
	}
	_ = device
}

// Owning the account is insufficient without both method-specific scopes.
func TestDeferredHistoryRequiresExplicitDeviceScopes(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{})
	token, _ := h.login(t, "owner", "chats:read", "chats:write")
	client := v1.NewDeviceTelemetryServiceClient(h.conn)
	request := &v1.UploadTelemetryRequest{Client: clientContext(), Records: []*v1.TelemetryRecord{{RecordId: uuid.NewString(), CollectionId: uuid.NewString(), CapturedAt: timestamppb.Now(), Value: &v1.TelemetryRecord_Status{Status: &v1.DeviceStatus{Model: "SYNTHETIC"}}}}}
	if _, err := client.UploadBatch(tokenContext(token), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal("chat-only token uploaded history", err)
	}
	if _, err := client.ListHistory(tokenContext(token), &v1.ListTelemetryRequest{Client: clientContext()}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("chat-only token read history", err)
	}
	if _, err := client.ListHistory(context.Background(), &v1.ListTelemetryRequest{Client: clientContext()}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("unauthenticated history read", err)
	}
}
