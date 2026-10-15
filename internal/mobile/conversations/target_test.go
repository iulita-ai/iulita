package conversations

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestTargetPinnedAcrossReplayRestartAndClear(t *testing.T) {
	r, path, owner, other, key := fixture(t)
	c := create(t, r, owner)
	in := input("same message")
	in.TargetDeviceID = uuid.NewString()
	turn, _, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.PhoneTargetAllowed(ctx, owner, c.ID, turn.ID, 1, in.TargetDeviceID); err != nil {
		t.Fatal(err)
	}
	if err = r.PhoneTargetAllowed(ctx, owner, c.ID, turn.ID, 1, uuid.NewString()); err == nil {
		t.Fatal("changed target accepted")
	}
	if err = r.PhoneTargetAllowed(ctx, other, c.ID, turn.ID, 1, in.TargetDeviceID); err == nil {
		t.Fatal("foreign origin accepted")
	}
	r.Close()
	r, err = OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	again, replay, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || !replay || again.TargetDeviceID != in.TargetDeviceID {
		t.Fatal("target not retained", err)
	}
	in.TargetDeviceID = uuid.NewString()
	if _, _, err = r.Accept(ctx, owner, c.ID, in); !errors.Is(err, ErrIdempotency) {
		t.Fatal("target mutation did not conflict", err)
	}
	if _, err = r.Clear(ctx, owner, c.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = r.PhoneTargetAllowed(ctx, owner, c.ID, turn.ID, 1, again.TargetDeviceID); err == nil {
		t.Fatal("clear failed to fence target")
	}
}

func TestDefaultTargetIsFrozenActorNotRetryingDevice(t *testing.T) {
	r, _, owner, _, _ := fixture(t)
	c := create(t, r, owner)
	in := input("ordinary message")
	in.ActorDeviceID = uuid.NewString()
	first, _, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.ActorDeviceID = uuid.NewString()
	again, replay, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil || !replay || again.TargetDeviceID != first.TargetDeviceID {
		t.Fatal("retry rebound actor", err)
	}
	if err = r.PhoneTargetAllowed(ctx, owner, c.ID, first.ID, 1, in.ActorDeviceID); err == nil {
		t.Fatal("retry device acquired phone target")
	}
}

// Rebuild v3 on disk, including an old digest and frozen caller authority.
func TestVersionThreeMigrationPreservesLegacyTargetAndReplay(t *testing.T) {
	r, path, owner, _, key := fixture(t)
	c := create(t, r, owner)
	in := input("legacy accepted input")
	turn, _, err := r.Accept(ctx, owner, c.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	device := uuid.NewString()
	if _, err = r.db.Exec(`UPDATE mobile_turns SET policy_snapshot=? WHERE id=?`, `{"Authority":{"DeviceID":"`+device+`"}}`, turn.ID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`ALTER TABLE mobile_turns DROP COLUMN target_device_id`, `DROP TABLE mobile_chat_schema`, `CREATE TABLE mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=3))`, `INSERT INTO mobile_chat_schema VALUES(3)`} {
		if _, err = r.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	reopened, err := OpenExisting(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	in.ActorDeviceID = uuid.NewString()
	again, replay, err := reopened.Accept(ctx, owner, c.ID, in)
	if err != nil || !replay || again.ID != turn.ID {
		t.Fatal("legacy digest changed", err)
	}
	if err = reopened.PhoneTargetAllowed(ctx, owner, c.ID, turn.ID, 1, device); err != nil {
		t.Fatal("frozen legacy caller lost", err)
	}
	if err = reopened.PhoneTargetAllowed(ctx, owner, c.ID, turn.ID, 1, in.ActorDeviceID); err == nil {
		t.Fatal("legacy target rebound to retry caller")
	}
}
