package conversations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// Reproduce the resource cycle deterministically: an external account writer
// needs authority reads while every conversation-writer connection is occupied.
func TestAccountAuthorityReadsSurviveWriterPoolExhaustion(t *testing.T) {
	r, path, owner, _, _ := fixture(t)
	conversation := create(t, r, owner)
	if _, _, err := r.Accept(ctx, owner, conversation.ID, input("phone")); err != nil {
		t.Fatal(err)
	}
	runtime, err := r.AcquireRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn, fence, err := r.Claim(ctx, runtime, owner)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256([]byte("approved phone action"))
	prompt, err := r.OpenPrompt(ctx, fence, Prompt{Kind: "confirmation", OperationID: uuid.NewString(), Fingerprint: fingerprint[:], RequiredScopes: []string{"devices:manage"}, FreshAuthentication: true, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	actor := ApprovalAuthority{OwnerID: owner, DeviceID: uuid.NewString(), FamilyID: uuid.NewString(), Scopes: []string{"devices:manage"}, AuthTime: time.Now().Unix()}
	if _, err = r.Decide(ctx, owner, actor.DeviceID, conversation.ID, turn.ID, prompt.ID, 1, "approve", prompt.OperationID, prompt.Nonce, fingerprint[:], actor); err != nil {
		t.Fatal(err)
	}
	account, err := sqlitedb.Open(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer account.Close()
	reservation, err := account.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Rollback()
	// Each held Conn models one BEGIN IMMEDIATE waiter without relying on sleeps
	// or waiting for SQLite's five-second busy timeout in the regression itself.
	writers := make([]*sql.Conn, 0, r.db.Stats().MaxOpenConnections)
	defer func() {
		for _, writer := range writers {
			writer.Close()
		}
	}()
	for range r.db.Stats().MaxOpenConnections {
		writer, connErr := r.db.Conn(ctx)
		if connErr != nil {
			t.Fatal(connErr)
		}
		writers = append(writers, writer)
	}
	if stats := r.db.Stats(); stats.InUse != stats.MaxOpenConnections {
		t.Fatal("writer pool was not exhausted", stats)
	}
	readContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = r.OriginAllowed(readContext, owner, conversation.ID, turn.ID, 1); err != nil {
		t.Fatal("account writer could not check origin with writer pool exhausted", err)
	}
	if err = r.DispatchAllowed(readContext, fence); err != nil {
		t.Fatal("dispatch authority was blocked by writer pool", err)
	}
	verified := 0
	verify := func(a ApprovalAuthority) error {
		verified++
		if a.FamilyID != actor.FamilyID {
			return errors.New("wrong approver")
		}
		return nil
	}
	if err = r.PhoneConsent(readContext, fence, prompt.OperationID, fingerprint[:], verify); err != nil || verified != 1 {
		t.Fatal("live approval could not be verified", err)
	}
	if err = r.OriginAllowed(readContext, owner, conversation.ID, turn.ID, 2); !errors.Is(err, ErrFenced) {
		t.Fatal("stale generation accepted", err)
	}
	if err = r.PhoneConsent(readContext, fence, prompt.OperationID, fingerprint[:], func(ApprovalAuthority) error { return errors.New("revoked") }); !errors.Is(err, ErrFenced) {
		t.Fatal("revoked approver accepted", err)
	}
	if _, err = reservation.ExecContext(ctx, `UPDATE users SET security_epoch=security_epoch+1 WHERE id=?`, owner); err != nil {
		t.Fatal(err)
	}
	if err = reservation.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = r.OriginAllowed(readContext, owner, conversation.ID, turn.ID, 1); !errors.Is(err, ErrFenced) {
		t.Fatal("read pool hid committed account revocation", err)
	}
	if err = r.DispatchAllowed(readContext, fence); !errors.Is(err, ErrFenced) {
		t.Fatal("dispatch survived account security change", err)
	}
	if readContext.Err() != nil {
		t.Fatal("authority check waited for a timeout", readContext.Err())
	}
}
