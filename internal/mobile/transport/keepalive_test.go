package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Three idle pings must preserve the same authenticated stream; a replacement
// unary connection is not proof. Keepalive must never keep revoked authority live.
func TestIdleKeepalivePreservesStreamButNotRevokedAuthority(t *testing.T) {
	h := setupHarness(t)
	token, _ := h.login(t, "owner")
	cert, err := x509.ParseCertificate(h.server.config.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	conn, err := grpc.NewClient(h.conn.Target(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, ServerName: "mobile.example.test", MinVersion: tls.VersionTLS13})), grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 31 * time.Second, Timeout: 5 * time.Second}), grpc.WithDisableRetry())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(tokenContext(token), 120*time.Second)
	defer cancel()
	sub := &v1.Subscription{Filter: &v1.Subscription_AllOwned{AllOwned: &emptypb.Empty{}}}
	snap, err := v1.NewChatServiceClient(h.conn).GetSnapshot(ctx, &v1.GetSnapshotRequest{Client: clientContext(), Subscription: sub})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := v1.NewChatServiceClient(conn).WatchEvents(ctx, &v1.WatchEventsRequest{Client: clientContext(), Subscription: sub, SnapshotId: snap.SnapshotId, Cursor: snap.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Header(); err != nil {
		t.Fatal(err)
	}
	type received struct {
		event *v1.ChatEvent
		err   error
	}
	incoming := make(chan received, 1)
	go func() { event, e := stream.Recv(); incoming <- received{event, e} }()
	select {
	case got := <-incoming:
		t.Fatalf("idle stream ended or emitted unexpected data: %v", got.err)
	case <-time.After(100 * time.Second):
	}
	created, err := v1.NewChatServiceClient(h.conn).CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "keepalive fixture", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ISOLATED}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-incoming:
		if got.err != nil || got.event.GetConversationId() != created.ConversationId {
			t.Fatal("same stream did not deliver after idle", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same stream lost events after idle")
	}
	if err = h.db.RevokeUserSessions(context.Background(), h.owners["owner"]); err != nil {
		t.Fatal(err)
	}
	go func() { event, e := stream.Recv(); incoming <- received{event, e} }()
	select {
	case got := <-incoming:
		if status.Code(got.err) != codes.Unauthenticated {
			t.Fatal("keepalive retained revoked authority", got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not interrupt live stream")
	}
}
