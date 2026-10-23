package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/uptrace/bun"
)

// LoadMCPRecords is used only at startup by the private MCP control plane.
func (s *Store) LoadMCPRecords(ctx context.Context) ([]domain.MCPRecord, error) {
	var rows []domain.MCPRecord
	err := s.db.NewSelect().Model(&rows).Scan(ctx)
	return rows, err
}

// GetMCPRecord resolves an exact owner-scoped encrypted record.
func (s *Store) GetMCPRecord(ctx context.Context, owner, id, kind string) (domain.MCPRecord, error) {
	var r domain.MCPRecord
	e := s.db.NewSelect().Model(&r).Where("owner_id = ? AND id = ? AND kind = ?", owner, id, kind).Scan(ctx)
	return r, e
}

// GetMCPOAuthFlow is the single narrowly scoped lookup needed before the OAuth
// callback has an authenticated owner. Its ID is a SHA-256 digest of random state.
func (s *Store) GetMCPOAuthFlow(ctx context.Context, id string) (domain.MCPRecord, error) {
	var r domain.MCPRecord
	e := s.db.NewSelect().Model(&r).Where("id = ? AND kind = 'oauth'", id).Scan(ctx)
	return r, e
}

// SaveMCPRecord atomically fences stale writers. expected=0 means insert only.
func (s *Store) SaveMCPRecord(ctx context.Context, r *domain.MCPRecord, expected int64) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if expected == 0 {
			if _, err := tx.NewInsert().Model(r).Exec(ctx); err != nil {
				return err
			}
		} else {
			result, err := tx.NewUpdate().Model(r).Column("payload", "revision").Where("id = ? AND owner_id = ? AND kind = ? AND revision = ?", r.ID, r.OwnerID, r.Kind, expected).Exec(ctx)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errors.New("MCP record revision conflict")
			}
		}
		event := &domain.MCPRecordEvent{OwnerID: r.OwnerID, ActorID: r.ActorID, RecordID: r.ID, Kind: r.Kind, Revision: r.Revision, CreatedAt: time.Now()}
		_, err := tx.NewInsert().Model(event).Exec(ctx)
		return err
	})
}

// DeleteMCPRecord removes expired encrypted artifacts without touching audit metadata.
func (s *Store) DeleteMCPRecord(ctx context.Context, owner, id string) error {
	res, err := s.db.NewDelete().Model((*domain.MCPRecord)(nil)).Where("id = ? AND owner_id = ?", id, owner).Exec(ctx)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// ListMCPEvents returns bounded safe audit metadata for one owner.
func (s *Store) ListMCPEvents(ctx context.Context, owner string) ([]domain.MCPRecordEvent, error) {
	var events []domain.MCPRecordEvent
	err := s.db.NewSelect().Model(&events).Where("owner_id = ?", owner).OrderExpr("id DESC").Limit(100).Scan(ctx)
	return events, err
}

// PruneMCPEvents removes expired safe audit metadata according to retention policy.
func (s *Store) PruneMCPEvents(ctx context.Context, before time.Time) error {
	_, err := s.db.NewDelete().Model((*domain.MCPRecordEvent)(nil)).Where("created_at < ?", before).Exec(ctx)
	return err
}

func (s *Store) migrateMCPIndexes(ctx context.Context) error {
	for _, stmt := range []string{
		"CREATE INDEX IF NOT EXISTS mcp_records_owner_kind ON mcp_records(owner_id,kind)",
		"CREATE INDEX IF NOT EXISTS mcp_events_owner_id ON mcp_record_events(owner_id,id)",
		"CREATE INDEX IF NOT EXISTS mcp_events_created ON mcp_record_events(created_at)",
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
