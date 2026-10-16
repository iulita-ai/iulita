package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"regexp"
	"time"

	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
)

var packageName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)

// OpenControl replaces the control stream and invalidates inherited native authority.
func (s *Store) OpenControl(ctx context.Context, identity Identity) (streamEpoch, serverSequence uint64, returnErr error) {
	var epoch, seq uint64
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:receive") || !oauthstore.HasScope(c, "devices:sync") {
			return ErrUnavailable
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO mobile_device_streams(owner_id,device_id,epoch,family_id,token_expires_at,expires_at) VALUES(?,?,1,?,?,?) ON CONFLICT(owner_id,device_id) DO UPDATE SET epoch=epoch+1,family_id=excluded.family_id,expires_at=excluded.expires_at,token_expires_at=excluded.token_expires_at,capabilities=NULL RETURNING epoch,server_seq`, c.Subject, c.DeviceID, c.FamilyID, c.ExpiresAt.UnixMilli(), min(time.Now().Add(10*time.Second).UnixMilli(), c.ExpiresAt.UnixMilli())).Scan(&epoch, &seq)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_native_sessions SET state='revoked' WHERE owner_id=? AND device_id=?`, c.Subject, c.DeviceID); err != nil {
			return err
		}
		// Break before make: no queued command, old session or lease is inherited.
		if checkErr3 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET state=CASE WHEN EXISTS(SELECT 1 FROM mobile_phone_commands x WHERE x.task_id=mobile_phone_tasks.id AND x.state IN ('dispatched','started')) THEN 'unknown_outcome' ELSE 'cancelled' END,cancellation_requested=1,reason=? WHERE owner_id=? AND device_id=? AND state IN ('queued','running','awaiting_confirmation','needs_user_action')`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, c.Subject, c.DeviceID); checkErr3 != nil {
			return checkErr3
		}
		_, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=CASE WHEN state IN ('dispatched','started') THEN 'unknown_outcome' ELSE 'cancelled' END WHERE owner_id=? AND device_id=? AND state IN ('queued','dispatched','started')`, c.Subject, c.DeviceID)
		return err
	})
	return epoch, seq, err
}

// TouchControl renews the current authenticated control lease.
func (s *Store) TouchControl(ctx context.Context, identity Identity, epoch uint64) error {
	return s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_device_streams SET expires_at=? WHERE owner_id=? AND device_id=? AND epoch=?`, min(time.Now().Add(10*time.Second).UnixMilli(), c.ExpiresAt.UnixMilli()), c.Subject, c.DeviceID, epoch)
		return err
	})
}

// CloseControl expires only the matching control-stream generation.
func (s *Store) CloseControl(ctx context.Context, owner, device string, epoch uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mobile_device_streams SET expires_at=0 WHERE owner_id=? AND device_id=? AND epoch=?`, owner, device, epoch)
	return err
}

// Capabilities records current capabilities and revokes reduced native authority.
func (s *Store) Capabilities(ctx context.Context, identity Identity, epoch uint64, capabilities *v1.DeviceCapabilities) error {
	if capabilities == nil || capabilities.ProtocolVersion != 1 || capabilities.PolicyRevision != PolicyRevision || capabilities.ReportedAt == nil || capabilities.ReportedAt.CheckValid() != nil || capabilities.ReportedAt.AsTime().Before(time.Now().Add(-time.Minute)) || capabilities.ReportedAt.AsTime().After(time.Now().Add(30*time.Second)) || len(capabilities.SupportedActions) > 16 || len(capabilities.GrantedCategories) > 4 {
		return ErrInvalid
	}
	seen := map[v1.ActionKind]bool{}
	requiresUI := capabilities.UiSafetyControlAvailable
	for _, kind := range capabilities.SupportedActions {
		if (!isSystem(kind) && (!isUI(kind) || !s.SupportsUI())) || seen[kind] {
			return ErrInvalid
		}
		seen[kind] = true
		requiresUI = requiresUI || isUI(kind)
	}
	categories := map[string]bool{}
	for _, category := range capabilities.GrantedCategories {
		if categories[category] || (category != "status" && category != "location" && category != "notifications" && category != "ui_tree") {
			return ErrInvalid
		}
		categories[category] = true
		requiresUI = requiresUI || category == "ui_tree"
	}
	// No native UI evidence can be inferred from a capabilities report. The
	// server's UI feature remains closed independently of this client boolean.
	body, err := proto.Marshal(capabilities)
	if err != nil {
		return ErrInvalid
	}
	return s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if requiresUI && !oauthstore.HasScope(c, "phone:ui") {
			return ErrUnavailable
		}
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE mobile_device_streams SET capabilities=? WHERE owner_id=? AND device_id=? AND epoch=?`, body, c.Subject, c.DeviceID, epoch)
		if err != nil {
			return err
		}
		// Capability reduction permanently closes the affected mirror. A later
		// regrant must establish new native authority, not revive an old session.
		rows, err := tx.QueryContext(ctx, `SELECT metadata FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND state='active'`, c.Subject, c.DeviceID)
		if err != nil {
			return err
		}
		var revoke []*v1.SessionMirror
		defer rows.Close() //nolint:errcheck // Fallback cleanup; explicit close below precedes writes.
		for rows.Next() {
			var data []byte
			if err = rows.Scan(&data); err != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return err
			}
			session := new(v1.SessionMirror)
			if proto.Unmarshal(data, session) != nil {
				rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
				return ErrInvalid
			}
			for _, kind := range session.AllowedActions {
				if !capabilityPermits(capabilities, kind) {
					revoke = append(revoke, session)
					break
				}
			}
		}
		err = rows.Err()
		rows.Close() //nolint:errcheck,gosec // Cleanup only; preserve the operation result.
		if err != nil {
			return err
		}
		for _, session := range revoke {
			if checkErr4 := s.endSessionIn(ctx, tx, c, epoch, session); checkErr4 != nil {
				return checkErr4
			}
		}
		return nil
	})
}

// Session accepts or revokes a bounded native session mirror.
func (s *Store) Session(ctx context.Context, identity Identity, epoch uint64, session *v1.SessionMirror) error {
	if session != nil && validID(session.SessionId) && session.SessionGeneration > 0 && (session.State == v1.SessionState_SESSION_STATE_REVOKED || session.State == v1.SessionState_SESSION_STATE_EXPIRED) {
		return s.endSession(ctx, identity, epoch, session)
	}
	if session == nil || (s.options.Clock != nil && session.ClockId != s.clockID()) || !validID(session.SessionId) || session.SessionGeneration == 0 || session.PolicyRevision != PolicyRevision || session.ExpiresAt == nil || session.ExpiresAt.CheckValid() != nil || (session.ScreenContentAllowed && (!s.SupportsUI() || len(session.ApprovedEnvelopeSha256) != 32)) || (session.ScreenshotAllowed && (!session.ScreenContentAllowed || !s.options.Vision)) || len(session.AllowedPackages) > 16 || len(session.AllowedActions) > 8 {
		return ErrInvalid
	}
	for _, p := range session.AllowedPackages {
		if len(p) > 200 || !packageName.MatchString(p) {
			return ErrInvalid
		}
	}
	if session.State != v1.SessionState_SESSION_STATE_ACTIVE && session.State != v1.SessionState_SESSION_STATE_REVOKED && session.State != v1.SessionState_SESSION_STATE_EXPIRED {
		return ErrInvalid
	}
	if !session.ExpiresAt.AsTime().After(time.Now()) || session.ExpiresAt.AsTime().After(time.Now().Add(15*time.Minute)) {
		return ErrInvalid
	}
	for _, kind := range session.AllowedActions {
		if !isSystem(kind) && (!isUI(kind) || !session.ScreenContentAllowed || !s.SupportsUI()) {
			return ErrUnavailable
		}
	}
	body, err := proto.Marshal(session)
	if err != nil {
		return ErrInvalid
	}
	return s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if (session.ScreenContentAllowed || session.ScreenshotAllowed) && !oauthstore.HasScope(c, "phone:ui") {
			return ErrUnavailable
		}
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		var capBytes []byte
		if tx.QueryRowContext(ctx, `SELECT capabilities FROM mobile_device_streams WHERE owner_id=? AND device_id=? AND epoch=?`, c.Subject, c.DeviceID, epoch).Scan(&capBytes) != nil {
			return ErrUnavailable
		}
		var capabilities v1.DeviceCapabilities
		if proto.Unmarshal(capBytes, &capabilities) != nil || capabilities.ProtocolVersion != 1 {
			return ErrUnavailable
		}
		for _, kind := range session.AllowedActions {
			found := false
			for _, supported := range capabilities.SupportedActions {
				if kind == supported {
					found = true
				}
			}
			if !found || !capabilityPermits(&capabilities, kind) {
				return ErrUnavailable
			}
		}
		var oldGeneration, oldEpoch uint64
		var oldState, oldFamily string
		var oldExpiry int64
		var oldBody []byte
		err := tx.QueryRowContext(ctx, `SELECT generation,stream_epoch,state,expires_at,metadata,family_id FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=?`, c.Subject, c.DeviceID, session.SessionId).Scan(&oldGeneration, &oldEpoch, &oldState, &oldExpiry, &oldBody, &oldFamily)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			var count int
			if e := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_native_sessions WHERE owner_id=? AND device_id=?`, c.Subject, c.DeviceID).Scan(&count); e != nil {
				return e
			}
			if count >= 500 {
				return ErrBusy
			}
		}
		if err == nil && (session.SessionGeneration < oldGeneration || (oldState != "active" && session.SessionGeneration == oldGeneration)) {
			return ErrEpoch
		}
		var original v1.SessionMirror
		if err == nil && oldEpoch != epoch {
			if proto.Unmarshal(oldBody, &original) != nil || !passiveContinuation(&original, session, oldFamily, c.FamilyID) {
				return ErrEpoch
			}
		}
		if err == nil && session.SessionGeneration == oldGeneration && (proto.Unmarshal(oldBody, &original) != nil || !proto.Equal(&original, session)) {
			return ErrInvalid
		}
		if err == nil && session.SessionGeneration == oldGeneration && session.ExpiresAt.AsTime().UnixMilli() > oldExpiry {
			return ErrInvalid
		}
		state := "active"
		if session.State != v1.SessionState_SESSION_STATE_ACTIVE {
			state = "revoked"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_native_sessions SET state='revoked' WHERE owner_id=? AND device_id=? AND id<>? AND state='active'`, c.Subject, c.DeviceID, session.SessionId); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mobile_native_sessions(owner_id,device_id,id,generation,stream_epoch,family_id,state,metadata,expires_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,device_id,id) DO UPDATE SET generation=excluded.generation,stream_epoch=excluded.stream_epoch,family_id=excluded.family_id,state=excluded.state,metadata=excluded.metadata,expires_at=excluded.expires_at`, c.Subject, c.DeviceID, session.SessionId, session.SessionGeneration, epoch, c.FamilyID, state, body, session.ExpiresAt.AsTime().UnixMilli())
		return err
	})
}

// Receipt records a command receipt without replaying its external effect.
func (s *Store) Receipt(ctx context.Context, identity Identity, epoch uint64, receipt *v1.CommandReceipt) error {
	if receipt == nil || !validID(receipt.ReceiptId) || !validID(receipt.CommandId) || !validID(receipt.TaskId) || len(receipt.PayloadDigestSha256) != 32 || receipt.State < v1.ReceiptState_RECEIPT_STATE_DELIVERED || receipt.State > v1.ReceiptState_RECEIPT_STATE_UNKNOWN_OUTCOME || receipt.OccurredAt == nil || receipt.OccurredAt.CheckValid() != nil || receipt.OccurredAt.AsTime().After(time.Now().Add(30*time.Second)) {
		return ErrInvalid
	}
	if receipt.State == v1.ReceiptState_RECEIPT_STATE_STARTED && !receipt.DispatchStarted {
		return ErrInvalid
	}
	if receipt.Reason < 0 || receipt.Reason > v1.ErrorReason_ERROR_REASON_REAUTHENTICATION_REQUIRED {
		return ErrInvalid
	}
	body, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(receipt)
	if marshalErr != nil {
		return marshalErr
	}
	digest := sha256.Sum256(body)
	return s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		var old []byte
		err := tx.QueryRowContext(ctx, `SELECT digest FROM mobile_phone_receipts WHERE id=? AND owner_id=? AND device_id=?`, receipt.ReceiptId, c.Subject, c.DeviceID).Scan(&old)
		if err == nil {
			if !bytes.Equal(old, digest[:]) {
				return ErrInvalid
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var expected []byte
		var seq, generation uint64
		var state string
		var created, expiry int64
		if err = tx.QueryRowContext(ctx, `SELECT x.digest,x.server_seq,t.session_generation,x.state,t.created_at,x.expires_at FROM mobile_phone_commands x JOIN mobile_phone_tasks t ON t.id=x.task_id WHERE x.id=? AND x.task_id=? AND x.owner_id=? AND x.device_id=?`, receipt.CommandId, receipt.TaskId, c.Subject, c.DeviceID).Scan(&expected, &seq, &generation, &state, &created, &expiry); err != nil {
			return ErrUnavailable
		}
		if !bytes.Equal(expected, receipt.PayloadDigestSha256) || seq != receipt.ServerSeq || generation != receipt.SessionGeneration {
			return ErrInvalid
		}
		if state == "queued" {
			return ErrInvalid
		}
		if created < time.Now().Add(-24*time.Hour).UnixMilli() || receipt.OccurredAt.AsTime().UnixMilli() < created-30000 {
			return ErrUnavailable
		}
		var receiptCount int
		if checkErr5 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_receipts WHERE command_id=?`, receipt.CommandId).Scan(&receiptCount); checkErr5 != nil {
			return checkErr5
		}
		if receiptCount >= 32 {
			return ErrBusy
		}
		uiTask := false
		var action v1.ActionKind
		if stepErr6 := tx.QueryRowContext(ctx, `SELECT action FROM mobile_phone_tasks WHERE id=?`, receipt.TaskId).Scan(&action); stepErr6 != nil {
			return stepErr6
		}
		uiTask = isUI(action)
		terminal := state == "succeeded" || state == "failed" || state == "cancelled" || state == "expired"
		commandState := state
		taskState := "running"
		switch receipt.State {
		case v1.ReceiptState_RECEIPT_STATE_STARTED:
			commandState = "started"
		case v1.ReceiptState_RECEIPT_STATE_SUCCEEDED:
			commandState = "succeeded"
			taskState = "succeeded"
			if uiTask {
				taskState = "running"
			}
		case v1.ReceiptState_RECEIPT_STATE_FAILED:
			commandState = "failed"
			taskState = "failed"
		case v1.ReceiptState_RECEIPT_STATE_CANCELLED:
			commandState = "cancelled"
			taskState = "cancelled"
		case v1.ReceiptState_RECEIPT_STATE_EXPIRED:
			commandState = "expired"
			taskState = "expired"
		case v1.ReceiptState_RECEIPT_STATE_UNKNOWN_OUTCOME:
			commandState = "unknown_outcome"
			taskState = "unknown_outcome"
		case v1.ReceiptState_RECEIPT_STATE_AWAITING_CONFIRMATION:
			taskState = "awaiting_confirmation"
		case v1.ReceiptState_RECEIPT_STATE_NEEDS_USER_ACTION:
			taskState = "needs_user_action"
		}
		if receipt.DispatchStarted && (commandState == "cancelled" || commandState == "expired") {
			commandState = "unknown_outcome"
			taskState = "unknown_outcome"
		}
		if receipt.OccurredAt.AsTime().UnixMilli() > expiry && (commandState == "started" || commandState == "dispatched") {
			commandState = "unknown_outcome"
			taskState = "unknown_outcome"
		}
		if terminal && commandState != state {
			return ErrInvalid
		}
		// Late evidence may resolve unknown_outcome but never creates a command,
		// restores a session/lease, or clears cancellation intent.
		if _, err = tx.ExecContext(ctx, `INSERT INTO mobile_phone_receipts VALUES(?,?,?,?,?,?)`, receipt.ReceiptId, c.Subject, c.DeviceID, receipt.CommandId, digest[:], time.Now().UnixMilli()); err != nil {
			return err
		}
		if !terminal && (state != "unknown_outcome" || (commandState != "unknown_outcome" && commandState != "started")) {
			if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state=? WHERE id=? AND owner_id=? AND device_id=?`, commandState, receipt.CommandId, c.Subject, c.DeviceID); err != nil {
				return err
			}
			// UI uncertainty and cancellation are terminal for orchestration. A late
			// receipt can reconcile command evidence, but cannot unlock another step.
			_, err = tx.ExecContext(ctx, `UPDATE mobile_phone_tasks SET state=?,reason=?,finished_at=CASE WHEN ?='succeeded' AND finished_at=0 THEN ? ELSE finished_at END WHERE id=? AND owner_id=? AND device_id=? AND (?=0 OR (cancellation_requested=0 AND state IN ('queued','running','awaiting_confirmation','needs_user_action')))`, taskState, receipt.Reason, taskState, time.Now().UnixMilli(), receipt.TaskId, c.Subject, c.DeviceID, uiTask)
			if err == nil {
				err = taskEvent(ctx, tx, receipt.TaskId)
			}
		}
		return err
	})
}

// A long-lived local telemetry choice may publish a fresh, short lease after
// token rotation. Never move UI authority, changed sources, a different OAuth
// family, or an explicit native Stop across transports. OpenControl still
// cancels old tasks/commands before this higher generation can be accepted.
func passiveContinuation(old, next *v1.SessionMirror, oldFamily, nextFamily string) bool {
	if oldFamily == "" || oldFamily != nextFamily || old.State != v1.SessionState_SESSION_STATE_ACTIVE || next.State != v1.SessionState_SESSION_STATE_ACTIVE || next.SessionGeneration <= old.SessionGeneration || len(old.AllowedActions) == 0 || old.ScreenContentAllowed || old.ScreenshotAllowed || len(old.ApprovedEnvelopeSha256) != 0 {
		return false
	}
	for _, action := range old.AllowedActions {
		if action != v1.ActionKind_ACTION_KIND_READ_STATUS && action != v1.ActionKind_ACTION_KIND_READ_LOCATION && action != v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
			return false
		}
	}
	a, ok := proto.Clone(old).(*v1.SessionMirror)
	if !ok {
		return false
	}
	b, ok := proto.Clone(next).(*v1.SessionMirror)
	if !ok {
		return false
	}
	a.SessionGeneration = 0
	b.SessionGeneration = 0
	a.ExpiresAt = nil
	b.ExpiresAt = nil
	a.ClockId = ""
	b.ClockId = ""
	return proto.Equal(a, b)
}
