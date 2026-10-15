package devices

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Task records the authority and outcome of a bounded phone operation.
type Task struct {
	ID, Owner, Device, Conversation, Turn, Session, State, Consent, Destination, CallerFamily, ViewerDevice string
	Generation, SessionGeneration                                                                           uint64
	Action                                                                                                  v1.ActionKind
	Cancelled                                                                                               bool
	Reason                                                                                                  v1.ErrorReason
	CreatedAt, ExpiresAt                                                                                    time.Time
	Approval                                                                                                *oauthstore.Authority
}

const taskColumns = `id,owner_id,device_id,conversation_id,turn_id,history_generation,action,state,cancellation_requested,reason,session_id,session_generation,created_at,expires_at,consent_revision,destination,caller_family_id,viewer_device_id,approval_authority`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var created, expiry int64
	var approval []byte
	err := row.Scan(&t.ID, &t.Owner, &t.Device, &t.Conversation, &t.Turn, &t.Generation, &t.Action, &t.State, &t.Cancelled, &t.Reason, &t.Session, &t.SessionGeneration, &created, &expiry, &t.Consent, &t.Destination, &t.CallerFamily, &t.ViewerDevice, &approval)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrUnavailable
	}
	if len(approval) > 0 {
		t.Approval = new(oauthstore.Authority)
		if json.Unmarshal(approval, t.Approval) != nil {
			return t, ErrInvalid
		}
	}
	t.CreatedAt = time.UnixMilli(created)
	t.ExpiresAt = time.UnixMilli(expiry)
	return t, err
}

// Draft describes a proposed phone task before admission.
type Draft struct {
	ID, Device, Viewer, CallerFamily string
	Kind                             v1.ActionKind
	System                           *v1.SystemAction
}

func (d Draft) validate() error {
	if !validID(d.ID) || !validID(d.Device) || !isSystem(d.Kind) {
		return ErrInvalid
	}
	if d.Kind <= v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
		if d.System != nil {
			return ErrInvalid
		}
		return nil
	}
	if d.System == nil {
		return ErrInvalid
	}
	switch body := d.System.Body.(type) {
	case *v1.SystemAction_TorchEnabled:
		if d.Kind != v1.ActionKind_ACTION_KIND_SET_TORCH {
			return ErrInvalid
		}
	case *v1.SystemAction_Navigation:
		if d.Kind != v1.ActionKind_ACTION_KIND_OPEN_NAVIGATION || body.Navigation == nil {
			return ErrInvalid
		}
		n := body.Navigation
		if math.IsNaN(n.Latitude) || math.IsNaN(n.Longitude) || math.IsInf(n.Latitude, 0) || math.IsInf(n.Longitude, 0) || n.Latitude < -90 || n.Latitude > 90 || n.Longitude < -180 || n.Longitude > 180 || len(n.Label) > 200 || !utf8.ValidString(n.Label) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Fingerprint returns the semantic digest of the proposed task.
func (d Draft) Fingerprint(f conversations.Fence) []byte {
	body := []byte(d.ID + "\x00" + d.Device + "\x00" + f.OwnerID + "\x00" + f.ConversationID + "\x00" + f.TurnID + "\x00mobile.result:" + d.Viewer + "\x00" + d.CallerFamily + "\x00" + d.Kind.String())
	if d.System != nil {
		encoded, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(d.System)
		if marshalErr != nil {
			return nil
		}
		body = append(body, encoded...)
	}
	sum := sha256.Sum256(body)
	return sum[:]
}

// Create atomically creates the owned resource after validating authority.
func (s *Store) Create(ctx context.Context, f conversations.Fence, d Draft) (Task, error) {
	if err := d.validate(); err != nil {
		return Task{}, err
	}
	authority := engine.AuthorityFrom(ctx)
	if authority.OwnerID != f.OwnerID {
		return Task{}, ErrUnavailable
	}
	required := "devices:read"
	if d.Kind > v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
		required = "devices:manage"
	}
	permitted := false
	for _, scope := range authority.Scopes {
		if scope == required {
			permitted = true
		}
	}
	if !permitted {
		return Task{}, ErrUnavailable
	}
	if required == "devices:manage" && (authority.AuthTime <= 0 || time.Unix(authority.AuthTime, 0).Before(time.Now().Add(-5*time.Minute))) {
		return Task{}, ErrUnavailable
	}
	if (d.Viewer != "" && d.Viewer != authority.DeviceID) || (d.CallerFamily != "" && d.CallerFamily != authority.FamilyID) {
		return Task{}, ErrInvalid
	}
	d.Viewer = authority.DeviceID
	d.CallerFamily = authority.FamilyID
	digest := d.Fingerprint(f)
	var result Task
	err := s.accounts.Guard(ctx, f.OwnerID, func(a browseroauth.Account) error {
		if a.Disabled || a.MustChangePassword || a.ID != f.OwnerID {
			return ErrUnavailable
		}
		if err := s.auth.SyncAccount(ctx, a.ID, a.CredentialVersion, true); err != nil {
			return err
		}
		if err := s.chats.DispatchAllowed(ctx, f); err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // Cleanup only; preserve the operation result.
		if err = s.auth.ValidateAuthorityIn(ctx, tx, authority, f.OwnerID); err != nil {
			return ErrUnavailable
		}
		var approval []byte
		if d.Kind != v1.ActionKind_ACTION_KIND_READ_STATUS {
			if checkErr3 := s.chats.PhoneConsent(ctx, f, d.ID, digest, func(a conversations.ApprovalAuthority) error {
				if stepErr1 := s.auth.ValidateAuthorityIn(ctx, tx, oauthstore.Authority(a), f.OwnerID); stepErr1 != nil {
					return stepErr1
				}
				var stepErr2 error
				approval, stepErr2 = json.Marshal(a)
				return stepErr2
			}); checkErr3 != nil {
				return checkErr3
			}
		}
		var old []byte
		err = tx.QueryRowContext(ctx, `SELECT request_digest FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND device_id=?`, d.ID, f.OwnerID, d.Device).Scan(&old)
		if err == nil {
			if !bytes.Equal(old, digest) {
				return ErrInvalid
			}
			result, err = scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND device_id=?`, d.ID, f.OwnerID, d.Device))
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var epoch uint64
		var receiverExpiry int64
		var caps []byte
		if tx.QueryRowContext(ctx, `SELECT s.epoch,s.token_expires_at,s.capabilities FROM mobile_device_streams s JOIN oauth_devices d ON d.owner_id=s.owner_id AND d.id=s.device_id WHERE s.owner_id=? AND s.device_id=? AND s.expires_at>? AND d.activated=1 AND d.revoked=0`, f.OwnerID, d.Device, time.Now().UnixMilli()).Scan(&epoch, &receiverExpiry, &caps) != nil {
			return ErrUnavailable
		}
		var capability v1.DeviceCapabilities
		if proto.Unmarshal(caps, &capability) != nil || capability.PolicyRevision != PolicyRevision {
			return ErrUnavailable
		}
		allowed := false
		for _, kind := range capability.SupportedActions {
			if d.Kind == kind {
				allowed = true
			}
		}
		if !allowed {
			return ErrUnavailable
		}
		var sessionBytes []byte
		var session string
		var sessionGeneration uint64
		if tx.QueryRowContext(ctx, `SELECT id,generation,metadata FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND state='active' AND stream_epoch=? AND expires_at>? ORDER BY id LIMIT 1`, f.OwnerID, d.Device, epoch, time.Now().UnixMilli()).Scan(&session, &sessionGeneration, &sessionBytes) != nil {
			return ErrUnavailable
		}
		var mirror v1.SessionMirror
		if proto.Unmarshal(sessionBytes, &mirror) != nil {
			return ErrUnavailable
		}
		if !nativeSessionLive(ctx, tx, Task{Owner: f.OwnerID, Device: d.Device, Session: session, SessionGeneration: sessionGeneration, Action: d.Kind}) {
			return ErrUnavailable
		}
		allowed = false
		for _, kind := range mirror.AllowedActions {
			if d.Kind == kind {
				allowed = true
			}
		}
		if !allowed {
			return ErrUnavailable
		}
		category := ""
		switch d.Kind {
		case v1.ActionKind_ACTION_KIND_READ_STATUS:
			category = "status"
		case v1.ActionKind_ACTION_KIND_READ_LOCATION:
			category = "location"
		case v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS:
			category = "notifications"
		}
		if category != "" {
			allowed = false
			for _, c := range capability.GrantedCategories {
				if c == category {
					allowed = true
				}
			}
			if !allowed {
				return ErrUnavailable
			}
		}
		var busy int
		if checkErr4 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_tasks WHERE owner_id=? AND device_id=? AND state IN ('queued','running','awaiting_confirmation','needs_user_action') AND expires_at>?`, f.OwnerID, d.Device, time.Now().UnixMilli()).Scan(&busy); checkErr4 != nil {
			return checkErr4
		}
		if busy >= 8 {
			return ErrBusy
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		expiry := now.Add(30 * time.Second)
		var familyExpiry int64
		if checkErr5 := tx.QueryRowContext(ctx, `SELECT expires_at FROM oauth_families WHERE id=? AND owner_id=?`, authority.FamilyID, f.OwnerID).Scan(&familyExpiry); checkErr5 != nil {
			return checkErr5
		}
		if bound := time.UnixMilli(receiverExpiry); bound.Before(expiry) {
			expiry = bound
		}
		if bound := time.Unix(familyExpiry, 0); bound.Before(expiry) {
			expiry = bound
		}
		if mirror.ExpiresAt.AsTime().Before(expiry) {
			expiry = mirror.ExpiresAt.AsTime()
		}
		var seq uint64
		if checkErr6 := tx.QueryRowContext(ctx, `UPDATE mobile_device_streams SET server_seq=server_seq+1 WHERE owner_id=? AND device_id=? AND epoch=? RETURNING server_seq`, f.OwnerID, d.Device, epoch).Scan(&seq); checkErr6 != nil {
			return checkErr6
		}
		commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(d.ID+":1")).String()
		command := &v1.Command{ProtocolVersion: 1, CommandId: commandID, TaskId: d.ID, DeviceId: d.Device, ServerSeq: seq, IssuedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(expiry), SessionId: session, SessionGeneration: sessionGeneration, CoordinatorEpoch: epoch, TaskStep: 1, Origin: &v1.TaskOrigin{ChannelType: "mobile", ChannelInstance: "mobile", BindingRevision: PolicyRevision, ConversationId: f.ConversationID, TurnId: f.TurnID}, Envelope: &v1.TaskEnvelope{Goal: d.Kind.String(), AllowedActions: []v1.ActionKind{d.Kind}, ExpiresAt: timestamppb.New(expiry)}}
		if d.System != nil {
			cloned := new(v1.SystemAction)
			proto.Merge(cloned, d.System)
			command.Action = &v1.Command_System{System: cloned}
		} else {
			command.Action = &v1.Command_Read{Read: &v1.ReadAction{Kind: d.Kind}}
		}
		canonical, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(command)
		if marshalErr != nil {
			return marshalErr
		}
		sum := sha256.Sum256(canonical)
		command.PayloadDigestSha256 = sum[:]
		payload, marshalErr := proto.Marshal(command)
		if marshalErr != nil {
			return marshalErr
		}
		binding := s.binding(f.OwnerID, d.Device, d.ID, "mobile.result:"+authority.DeviceID, PolicyRevision, "command")
		if checkErr7 := s.content.Put(commandID, binding, payload, expiry); checkErr7 != nil {
			return checkErr7
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mobile_phone_tasks(id,owner_id,device_id,conversation_id,turn_id,history_generation,action,request_digest,caller_family_id,viewer_device_id,state,session_id,session_generation,created_at,expires_at,consent_revision,destination,approval_authority) VALUES(?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?)`, d.ID, f.OwnerID, d.Device, f.ConversationID, f.TurnID, f.Generation, d.Kind, digest, authority.FamilyID, authority.DeviceID, session, sessionGeneration, now.UnixMilli(), expiry.UnixMilli(), PolicyRevision, "mobile.result:"+authority.DeviceID, approval)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mobile_phone_commands(id,task_id,owner_id,device_id,server_seq,digest,state,stream_epoch,coordinator_epoch,expires_at,payload_ref) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`, commandID, d.ID, f.OwnerID, d.Device, seq, sum[:], epoch, epoch, expiry.UnixMilli(), commandID)
		if err != nil {
			return err
		}
		if checkErr8 := taskEvent(ctx, tx, d.ID); checkErr8 != nil {
			return checkErr8
		}
		if checkErr9 := tx.Commit(); checkErr9 != nil {
			return checkErr9
		}
		result = Task{ID: d.ID, Owner: f.OwnerID, Device: d.Device, Conversation: f.ConversationID, Turn: f.TurnID, Generation: f.Generation, Session: session, SessionGeneration: sessionGeneration, State: "queued", Action: d.Kind, Consent: PolicyRevision, Destination: "mobile.result:" + authority.DeviceID, ViewerDevice: authority.DeviceID, CallerFamily: authority.FamilyID, CreatedAt: now, ExpiresAt: expiry}
		return nil
	})
	return result, err
}

// GetTask returns an owned task after validating current access.
func (s *Store) GetTask(ctx context.Context, identity Identity, id string) (Task, error) {
	var task Task
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		var err error
		task, err = scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=?`, id, c.Subject))
		if errors.Is(err, ErrUnavailable) {
			return conversations.ErrNotFound
		}
		return err
	})
	return task, err
}

// Next claims the next command only while dispatch authority remains valid.
func (s *Store) Next(ctx context.Context, identity Identity, epoch uint64) (*v1.Command, error) {
	var command *v1.Command
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "devices:receive") {
			return ErrUnavailable
		}
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		var commandID, taskID, ref string
		err := tx.QueryRowContext(ctx, `SELECT id,task_id,payload_ref FROM mobile_phone_commands WHERE owner_id=? AND device_id=? AND stream_epoch=? AND state='queued' ORDER BY server_seq LIMIT 1`, c.Subject, c.DeviceID, epoch).Scan(&commandID, &taskID, &ref)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM mobile_phone_tasks WHERE id=? AND owner_id=? AND device_id=?`, taskID, c.Subject, c.DeviceID))
		if err != nil {
			return err
		}
		var live int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=? AND generation=? AND stream_epoch=? AND state='active' AND expires_at>?`, c.Subject, c.DeviceID, task.Session, task.SessionGeneration, epoch, time.Now().UnixMilli()).Scan(&live)
		if err != nil {
			return err
		}
		var capabilityBytes []byte
		var capability v1.DeviceCapabilities
		currentCapability := tx.QueryRowContext(ctx, `SELECT capabilities FROM mobile_device_streams WHERE owner_id=? AND device_id=? AND epoch=?`, task.Owner, task.Device, epoch).Scan(&capabilityBytes) == nil && proto.Unmarshal(capabilityBytes, &capability) == nil && capability.PolicyRevision == PolicyRevision
		allowed := false
		if currentCapability {
			for _, kind := range capability.SupportedActions {
				if kind == task.Action {
					allowed = true
				}
			}
		}
		if task.Action <= v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS {
			category := map[v1.ActionKind]string{v1.ActionKind_ACTION_KIND_READ_STATUS: "status", v1.ActionKind_ACTION_KIND_READ_LOCATION: "location", v1.ActionKind_ACTION_KIND_READ_NOTIFICATIONS: "notifications"}[task.Action]
			granted := false
			for _, c := range capability.GrantedCategories {
				if c == category {
					granted = true
				}
			}
			allowed = allowed && granted
		}
		invalid := !allowed || task.Cancelled || !task.ExpiresAt.After(time.Now()) || live != 1 || !nativeSessionLive(ctx, tx, task) || !s.originFamilyLive(ctx, tx, task) || s.chats.OriginAllowed(ctx, task.Owner, task.Conversation, task.Turn, task.Generation) != nil
		payload, _, bodyErr := s.content.Get(ref, s.binding(task.Owner, task.Device, task.ID, task.Destination, task.Consent, "command"))
		if invalid || bodyErr != nil {
			if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state='expired' WHERE id=?`, commandID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_tasks SET state='expired',reason=? WHERE id=?`, v1.ErrorReason_ERROR_REASON_DEVICE_UNAVAILABLE, taskID); err != nil {
				return err
			}
			return taskEvent(ctx, tx, taskID)
		}
		defer clear(payload)
		candidate := new(v1.Command)
		if proto.Unmarshal(payload, candidate) != nil {
			return ErrInvalid
		}
		// Publication is a durable dispatch boundary. A lost Send/ACK cannot
		// return this row to queued; native receipts reconcile it by command ID.
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state='dispatched' WHERE id=? AND state='queued'`, commandID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_tasks SET state='running' WHERE id=?`, taskID); err != nil {
			return err
		}
		command = candidate
		return taskEvent(ctx, tx, taskID)
	})
	return command, err
}

// DispatchLost marks uncertain dispatch without replaying the command.
func (s *Store) DispatchLost(ctx context.Context, owner, device, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Fallback rollback after checked commit.
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_tasks SET state='unknown_outcome',reason=? WHERE id IN (SELECT task_id FROM mobile_phone_commands WHERE owner_id=? AND device_id=? AND id=? AND state IN ('dispatched','started'))`, v1.ErrorReason_ERROR_REASON_UNKNOWN_OUTCOME, owner, device, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mobile_phone_commands SET state='unknown_outcome' WHERE owner_id=? AND device_id=? AND id=? AND state IN ('dispatched','started')`, owner, device, id); err != nil {
		return err
	}
	if checkErr10 := taskEventForCommand(ctx, tx, owner, device, id); checkErr10 != nil {
		return checkErr10
	}
	return tx.Commit()
}
