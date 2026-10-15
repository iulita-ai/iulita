package conversations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSaturatedResponseCacheKeepsSafetyAndDedup(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	id := uuid.NewString()
	_, err := r.db.Exec(`INSERT INTO mobile_operations(owner_id,client_id,method,id,digest,response,created_at) VALUES(?,?,?, ?,zeroblob(32),zeroblob(16777216),?)`, owner, "mobile", "CreateConversation", uuid.NewString(), time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	fn := func(context.Context) ([]byte, error) { called++; return []byte("result"), nil }
	if _, _, e := r.Operation(ctx, owner, "mobile", "SendMessage", uuid.NewString(), []byte("input"), fn); !errors.Is(e, ErrQueueFull) || called != 0 {
		t.Fatal("ordinary saturation", e, called)
	}
	out, dupe, e := r.Operation(ctx, owner, "mobile", "CancelTurn", id, []byte("one"), fn)
	if e != nil || dupe || string(out) != "result" || called != 1 {
		t.Fatal("safety blocked", e, called)
	}
	if _, _, e = r.Operation(ctx, owner, "mobile", "CancelTurn", id, []byte("one"), fn); !errors.Is(e, ErrResync) || called != 1 {
		t.Fatal("redacted replay", e, called)
	}
	if _, _, e = r.Operation(ctx, owner, "mobile", "CancelTurn", id, []byte("changed"), fn); !errors.Is(e, ErrIdempotency) || called != 1 {
		t.Fatal("rebind", e, called)
	}
	if _, e = r.db.Exec(`UPDATE mobile_operations SET created_at=? WHERE method='CreateConversation'`, time.Now().Add(-8*24*time.Hour).Unix()); e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.Operation(ctx, owner, "mobile", "SendMessage", uuid.NewString(), []byte("new"), fn); e != nil {
		t.Fatal("aged response blocks forever", e)
	}
	if _, _, e = r.Operation(ctx, owner, "mobile", "CancelTurn", id, []byte("one"), fn); !errors.Is(e, ErrResync) || called != 2 {
		t.Fatal("old safety replay after cache cleanup", e, called)
	}
}
