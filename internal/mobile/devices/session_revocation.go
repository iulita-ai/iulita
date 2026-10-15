package devices

import (
	"context"
	"database/sql"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
)

func nativeSessionLive(ctx context.Context, tx *sql.Tx, t Task) bool {
	var capabilities []byte
	now := time.Now()
	// A mirror cannot outlive the source transport or its authenticated token.
	// This also fences reads and provider transfer by a separately live viewer.
	err := tx.QueryRowContext(ctx, `SELECT control.capabilities FROM mobile_native_sessions s JOIN oauth_devices d ON d.owner_id=s.owner_id AND d.id=s.device_id JOIN oauth_families f ON f.id=s.family_id AND f.owner_id=s.owner_id AND f.device_id=s.device_id JOIN oauth_owners o ON o.id=f.owner_id JOIN oauth_realm r ON r.signing_epoch=f.signing_epoch JOIN mobile_device_streams control ON control.owner_id=s.owner_id AND control.device_id=s.device_id AND control.epoch=s.stream_epoch AND control.family_id=s.family_id WHERE s.owner_id=? AND s.device_id=? AND s.id=? AND s.generation=? AND s.state='active' AND s.expires_at>? AND control.expires_at>? AND control.token_expires_at>? AND d.revoked=0 AND d.activated=1 AND f.revoked=0 AND f.stage='active' AND f.expires_at>? AND f.owner_epoch=o.epoch AND f.device_epoch=d.epoch`, t.Owner, t.Device, t.Session, t.SessionGeneration, now.UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.Unix()).Scan(&capabilities)
	var reportedCapabilities v1.DeviceCapabilities
	return err == nil && proto.Unmarshal(capabilities, &reportedCapabilities) == nil && capabilityPermits(&reportedCapabilities, t.Action)
}

func capabilityPermits(capabilities *v1.DeviceCapabilities, kind v1.ActionKind) bool {
	if capabilities == nil || capabilities.ProtocolVersion != 1 || capabilities.PolicyRevision != PolicyRevision {
		return false
	}
	supported := false
	for _, allowed := range capabilities.SupportedActions {
		if kind == allowed {
			supported = true
		}
	}
	if !supported {
		return false
	}
	category := ""
	switch kind {
	case v1.ActionKind_ACTION_KIND_READ_STATUS:
		category = "status"
	case v1.ActionKind_ACTION_KIND_READ_LOCATION:
		category = "location"
	case v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS:
		category = "notifications"
	}
	if isUI(kind) {
		category = "ui_tree"
		if !capabilities.UiSafetyControlAvailable {
			return false
		}
	}
	if category == "" {
		return isSystem(kind)
	}
	for _, granted := range capabilities.GrantedCategories {
		if category == granted {
			return true
		}
	}
	return false
}

// A native Stop report only reduces authority. It does not need fresh grant
// capabilities, an unexpired approval or echoed screen-content fields.
func (s *Store) endSession(ctx context.Context, identity Identity, epoch uint64, session *v1.SessionMirror) error {
	return s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error { return s.endSessionIn(ctx, tx, c, epoch, session) })
}
func (s *Store) endSessionIn(ctx context.Context, tx *sql.Tx, c *oauthstore.Claims, epoch uint64, session *v1.SessionMirror) error {
	if err := streamLive(ctx, tx, c, epoch); err != nil {
		return err
	}
	var generation, streamEpoch uint64
	if tx.QueryRowContext(ctx, `SELECT generation,stream_epoch FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=?`, c.Subject, c.DeviceID, session.SessionId).Scan(&generation, &streamEpoch) != nil {
		return ErrUnavailable
	}
	if streamEpoch != epoch || session.SessionGeneration < generation {
		return ErrEpoch
	}
	body, marshalErr := proto.Marshal(&v1.SessionMirror{SessionId: session.SessionId, SessionGeneration: session.SessionGeneration, State: v1.SessionState_SESSION_STATE_REVOKED, PolicyRevision: PolicyRevision})
	if marshalErr != nil {
		return marshalErr
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mobile_native_sessions SET state='revoked',generation=?,metadata=?,expires_at=? WHERE owner_id=? AND device_id=? AND id=?`, session.SessionGeneration, body, time.Now().UnixMilli(), c.Subject, c.DeviceID, session.SessionId); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM mobile_phone_tasks WHERE owner_id=? AND device_id=? AND session_id=?`, c.Subject, c.DeviceID, session.SessionId)
	if err != nil {
		return err
	}
	var tasks []string
	defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
			return err
		}
		tasks = append(tasks, id)
	}
	err = rows.Err()
	rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
	if err != nil {
		return err
	}
	for _, id := range tasks {
		s.content.DeleteTask(c.Subject, id)
	}
	if checkErr3 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET cancellation_requested=1,state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands x WHERE x.task_id=mobile_phone_tasks.id AND x.state IN ('dispatched','started','unknown_outcome')) THEN 'unknown_outcome' ELSE 'cancelled' END,reason=? WHERE owner_id=? AND device_id=? AND session_id=? AND state IN ('queued','running','awaiting_confirmation','needs_user_action')`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, c.Subject, c.DeviceID, session.SessionId); checkErr3 != nil {
		return checkErr3
	}
	_, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END WHERE task_id IN (SELECT id FROM mobile_phone_tasks WHERE owner_id=? AND device_id=? AND session_id=?) AND state IN ('queued','dispatched','started')`, c.Subject, c.DeviceID, session.SessionId)
	return err
}
