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
	r := &domain.MCPRecord{ID: "record", OwnerID: "owner", ActorID: "owner", Kind: "connection", Action: "connection_created", State: "draft:stopped", Revision: 1, Payload: "encrypted-v1"}
	if e = s.SaveMCPRecord(ctx, r, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetMCPRecord(ctx, "other", r.ID, "connection"); e == nil {
		t.Fatal("foreign record readable")
	}
	events, e := s.ListMCPEvents(ctx, "owner")
	if e != nil || len(events) != 1 || events[0].ActorID != "owner" || events[0].Action != "connection_created" || events[0].State != "draft:stopped" {
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

func TestMCPAuditMigrationPreservesOldEvents(t *testing.T) {
	ctx := context.Background()
	s, e := New(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, e = s.db.ExecContext(ctx, `CREATE TABLE mcp_record_events (id INTEGER PRIMARY KEY, owner_id TEXT NOT NULL, actor_id TEXT NOT NULL, record_id TEXT NOT NULL, kind TEXT NOT NULL, revision INTEGER NOT NULL, created_at TIMESTAMP NOT NULL)`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO mcp_record_events VALUES (1,'owner','owner','record','connection',1,CURRENT_TIMESTAMP)`)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e = s.RunMigrations(ctx); e != nil {
			t.Fatal(e)
		}
	}
	events, e := s.ListMCPEvents(ctx, "owner")
	if e != nil || len(events) != 1 || events[0].RecordID != "record" || events[0].Action != "" {
		t.Fatal(events, e)
	}
}
