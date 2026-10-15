package transport

import (
	"context"
	"testing"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestReceiptAckWaitsForAccountWriteReservation(t *testing.T) {
	h := setupHarness(t)
	token, device := h.login(t, "owner")
	native, command := statusTask(t, h, token, device)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accounts, err := sqlitedb.Open(ctx, h.paths[0], false)
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	reservation, err := accounts.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Rollback()
	// Model real writer contention beyond the old one-second simulator budget.
	// Receipt admission must still wait for current account authority to commit.
	const hold = 1250 * time.Millisecond
	committed := make(chan error, 1)
	release := time.AfterFunc(hold, func() { committed <- reservation.Commit() })
	defer release.Stop()
	started := time.Now()
	native.receipt(t, command, v1.ReceiptState_RECEIPT_STATE_SUCCEEDED)
	if time.Since(started) < hold {
		t.Fatal("receipt acknowledged before the account write reservation completed")
	}
	if err = <-committed; err != nil {
		t.Fatal(err)
	}
	client := v1.NewDeviceServiceClient(h.conn)
	if _, err = client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId}); err != nil {
		t.Fatal("valid source authority lost during receipt contention", err)
	}
	if err = h.db.RevokeUserSessions(ctx, h.owners["owner"]); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetTaskResult(tokenContext(token), &v1.GetTaskResultRequest{Client: clientContext(), TaskId: command.TaskId}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("account revocation did not fence the receipt result", err)
	}
}
