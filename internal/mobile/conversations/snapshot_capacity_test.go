package conversations

import (
	"errors"
	"testing"
)

func TestSnapshotCapacityRetiresOldestWithoutBlockingRefreshOrOtherOwners(t *testing.T) {
	r, _, owner, other, _ := fixture(t)
	_ = create(t, r, owner)
	_ = create(t, r, owner)
	_ = create(t, r, other)
	foreign, err := r.Snapshot(ctx, other, nil, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshots := make([]Snapshot, 0, 33)
	for i := 0; i < 33; i++ {
		s, e := r.Snapshot(ctx, owner, nil, 1, "")
		if e != nil {
			t.Fatalf("refresh %d: %v", i, e)
		}
		snapshots = append(snapshots, s)
	}
	var count int
	if err = r.db.QueryRow(`SELECT COUNT(*) FROM mobile_snapshots WHERE owner_id=?`, owner).Scan(&count); err != nil || count != 32 {
		t.Fatalf("owner quota: count=%d err=%v", count, err)
	}
	first := snapshots[0]
	if _, _, err = r.Events(ctx, owner, nil, first.ID, first.Cursor, 10); !errors.Is(err, ErrResync) {
		t.Fatalf("retired event cursor accepted: %v", err)
	}
	if _, err = r.Snapshot(ctx, owner, nil, 1, first.NextPage); !errors.Is(err, ErrResync) {
		t.Fatalf("retired page cursor accepted: %v", err)
	}
	for _, s := range []Snapshot{snapshots[1], snapshots[32]} {
		if _, _, err = r.Events(ctx, owner, nil, s.ID, s.Cursor, 10); err != nil {
			t.Fatalf("retained cursor rejected: %v", err)
		}
	}
	if _, _, err = r.Events(ctx, other, nil, foreign.ID, foreign.Cursor, 10); err != nil {
		t.Fatalf("other owner evicted: %v", err)
	}
	if _, _, err = r.Events(ctx, other, nil, snapshots[32].ID, snapshots[32].Cursor, 10); !errors.Is(err, ErrResync) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
}
