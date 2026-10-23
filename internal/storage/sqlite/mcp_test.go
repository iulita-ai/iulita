package sqlite

import (
	"context"
	"testing"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestMCPRecordCASAndAuditAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.RunMigrations(ctx); e != nil {
		t.Fatal(e)
	}
	r := &domain.MCPRecord{ID: "record", OwnerID: "owner", ActorID: "owner", Kind: "connection", Revision: 1, Payload: "encrypted-v1"}
	if e = s.SaveMCPRecord(ctx, r, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetMCPRecord(ctx, "other", r.ID, "connection"); e == nil {
		t.Fatal("foreign record readable")
	}
	events, e := s.ListMCPEvents(ctx, "owner")
	if e != nil || len(events) != 1 || events[0].ActorID != "owner" {
		t.Fatal(events, e)
	}
	r.Revision = 2
	r.Payload = "encrypted-v2"
	if e = s.SaveMCPRecord(ctx, r, 7); e == nil {
		t.Fatal("stale writer accepted")
	}
	events, _ = s.ListMCPEvents(ctx, "owner")
	if len(events) != 1 {
		t.Fatal("failed CAS created audit event")
	}
	if _, e = s.db.ExecContext(ctx, "DROP TABLE mcp_record_events"); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveMCPRecord(ctx, r, 1); e == nil {
		t.Fatal("record changed without audit")
	}
	old, e := s.GetMCPRecord(ctx, "owner", r.ID, "connection")
	if e != nil || old.Revision != 1 || old.Payload != "encrypted-v1" {
		t.Fatal("transaction failed to roll back", old, e)
	}
}
