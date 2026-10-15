package devices

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
)

func (s *Store) originFamilyLive(ctx context.Context, tx *sql.Tx, t Task) bool {
	var authTime int64
	err := tx.QueryRowContext(ctx, `SELECT f.auth_time FROM oauth_families f JOIN oauth_owners o ON o.id=f.owner_id JOIN oauth_devices d ON d.id=f.device_id AND d.owner_id=f.owner_id JOIN oauth_realm r ON r.signing_epoch=f.signing_epoch WHERE f.id=? AND f.owner_id=? AND f.stage='active' AND f.revoked=0 AND f.expires_at>? AND f.owner_epoch=o.epoch AND f.device_epoch=d.epoch AND d.revoked=0`, t.CallerFamily, t.Owner, time.Now().Unix()).Scan(&authTime)
	if err != nil {
		return false
	}
	if t.Action != v1.ActionKind_ACTION_KIND_READ_STATUS {
		if t.Approval == nil || s.auth.ValidateAuthorityIn(ctx, tx, *t.Approval, t.Owner) != nil {
			return false
		}
		if t.Action > v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS && (time.Unix(authTime, 0).Before(time.Now().Add(-5*time.Minute)) || time.Unix(t.Approval.AuthTime, 0).Before(time.Now().Add(-5*time.Minute))) {
			return false
		}
	}
	return true
}
