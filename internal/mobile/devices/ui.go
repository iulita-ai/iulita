package devices

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Transition selects only a manifest-owned resource and envelope-owned text.
type Transition struct {
	Kind                                                  v1.ActionKind
	ResourceID, ClassName, TextField, RecipientResourceID string
}

// Scenario pins one tested app/layout and bounded deterministic transitions.
type Scenario struct {
	Binding                             *v1.ScenarioBinding
	Goal                                string
	Reasoning                           bool
	Steps                               []Transition
	OutcomeResourceID, OutcomeTextField string
}

func isUI(k v1.ActionKind) bool {
	return k >= v1.ActionKind_ACTION_KIND_UI_OBSERVE && k <= v1.ActionKind_ACTION_KIND_UI_SUBMIT
}
func (m Scenario) validate() error {
	b := m.Binding
	if b == nil || b.ScenarioId == "" || b.Version == 0 || !packageName.MatchString(b.PackageName) || len(b.SignerSha256) != 32 || b.SupportedAppVersion == "" || b.UiLanguage == "" || b.LayoutRevision == "" || m.Goal == "" || len(m.Steps) == 0 || len(m.Steps) > 8 || m.OutcomeResourceID == "" {
		return ErrInvalid
	}
	for _, step := range m.Steps {
		if !isUI(step.Kind) || step.Kind == v1.ActionKind_ACTION_KIND_UI_OBSERVE || step.ResourceID == "" || step.ClassName == "" || step.Kind == v1.ActionKind_ACTION_KIND_UI_SET_TEXT && step.TextField != "target_object" && step.TextField != "recipient" {
			return ErrInvalid
		}
	}
	if m.OutcomeTextField != "target_object" && m.OutcomeTextField != "recipient" && m.OutcomeTextField != "" {
		return ErrInvalid
	}
	return nil
}
func (m Scenario) clone() Scenario {
	b := new(v1.ScenarioBinding)
	proto.Merge(b, m.Binding)
	m.Binding = b
	m.Steps = append([]Transition(nil), m.Steps...)
	return m
}
func (s *Store) scenario(b *v1.ScenarioBinding) (Scenario, error) {
	if !s.SupportsUI() {
		return Scenario{}, ErrUnavailable
	}
	for _, m := range s.options.Scenarios {
		if proto.Equal(m.Binding, b) {
			return m, nil
		}
	}
	return Scenario{}, ErrUnavailable
}
func manifestDigest(m Scenario) []byte {
	body, err := metadata(m)
	if err != nil {
		return nil
	}
	return sha256Bytes(body)
}
func field(e *v1.TaskEnvelope, name string) string {
	if name == "recipient" {
		return e.Recipient
	}
	if name == "target_object" {
		return e.TargetObject
	}
	return ""
}
func (s *Store) envelope(t Task) (*v1.TaskEnvelope, error) {
	body, _, err := s.content.Get("ui-envelope:"+t.ID, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "command"))
	if err != nil {
		return nil, err
	}
	defer clear(body)
	e := new(v1.TaskEnvelope)
	if proto.Unmarshal(body, e) != nil {
		return nil, ErrUnavailable
	}
	return e, nil
}

// CreateUI admits one explicitly approved native-envelope-bound UI task.
func (s *Store) CreateUI(ctx context.Context, identity Identity, in *v1.CreateUITaskRequest) (Task, error) {
	if in == nil || in.Operation == nil || !validID(in.Operation.OperationId) || !validID(in.DeviceId) || !validID(in.ConversationId) || !validID(in.TurnId) || in.Envelope == nil {
		return Task{}, ErrInvalid
	}
	e := in.Envelope
	manifest, err := s.scenario(e.Scenario)
	if err != nil {
		return Task{}, err
	}
	if e.Goal != manifest.Goal || len(e.TargetObject) > 4096 || len(e.Recipient) > 256 || !utf8.ValidString(e.TargetObject+e.Recipient) || e.ExpiresAt == nil || e.ExpiresAt.CheckValid() != nil || !e.ExpiresAt.AsTime().After(s.clock()) || e.ExpiresAt.AsTime().After(s.clock().Add(2*time.Minute)) {
		return Task{}, ErrInvalid
	}
	digest, err := semantic(e)
	if err != nil || !bytes.Equal(digest, in.ApprovedEnvelopeSha256) {
		return Task{}, ErrInvalid
	}
	allowed := map[v1.ActionKind]bool{}
	for _, a := range e.AllowedActions {
		if !isUI(a) || allowed[a] {
			return Task{}, ErrInvalid
		}
		allowed[a] = true
	}
	if !allowed[v1.ActionKind_ACTION_KIND_UI_OBSERVE] {
		return Task{}, ErrInvalid
	}
	for _, step := range manifest.Steps {
		if !allowed[step.Kind] {
			return Task{}, ErrInvalid
		}
	}
	requestDigest, err := semantic(&v1.CreateUITaskRequest{DeviceId: in.DeviceId, ConversationId: in.ConversationId, TurnId: in.TurnId, HistoryGeneration: in.HistoryGeneration, Envelope: e})
	if err != nil {
		return Task{}, err
	}
	var result Task
	err = s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if !oauthstore.HasScope(c, "phone:ui") || !oauthstore.HasScope(c, "devices:manage") || !s.auth.Fresh(c) {
			return ErrUnavailable
		}
		if stepErr1 := s.chats.PhoneTargetAllowed(ctx, c.Subject, in.ConversationId, in.TurnId, in.HistoryGeneration, in.DeviceId); stepErr1 != nil {
			return stepErr1
		}
		var existing, viewer string
		var old []byte
		lookup := tx.QueryRowContext(ctx, `SELECT id,viewer_device_id,request_digest FROM mobile_phone_tasks WHERE owner_id=? AND id=?`, c.Subject, in.Operation.OperationId).Scan(&existing, &viewer, &old)
		if lookup == nil {
			if !bytes.Equal(old, requestDigest) || viewer != c.DeviceID {
				return ErrInvalid
			}
			var stepErr2 error
			result, stepErr2 = taskIn(ctx, tx, c.Subject, existing)
			return stepErr2
		}
		if lookup != sql.ErrNoRows {
			return lookup
		}
		var sessionID string
		var generation, epoch uint64
		var sessionBody []byte
		if tx.QueryRowContext(ctx, `SELECT n.id,n.generation,n.stream_epoch,n.metadata FROM mobile_native_sessions n JOIN mobile_device_streams d ON d.owner_id=n.owner_id AND d.device_id=n.device_id AND d.epoch=n.stream_epoch WHERE n.owner_id=? AND n.device_id=? AND n.state='active' AND n.expires_at>? AND d.expires_at>?`, c.Subject, in.DeviceId, s.clock().UnixMilli(), s.clock().UnixMilli()).Scan(&sessionID, &generation, &epoch, &sessionBody) != nil {
			return ErrUnavailable
		}
		mirror := new(v1.SessionMirror)
		if proto.Unmarshal(sessionBody, mirror) != nil || !mirror.ScreenContentAllowed || !bytes.Equal(mirror.ApprovedEnvelopeSha256, digest) {
			return ErrUnavailable
		}
		packages := false
		for _, name := range mirror.AllowedPackages {
			if name == e.Scenario.PackageName {
				packages = true
			}
		}
		if !packages {
			return ErrUnavailable
		}
		grants := map[v1.ActionKind]bool{}
		for _, kind := range mirror.AllowedActions {
			grants[kind] = true
		}
		for kind := range allowed {
			if !grants[kind] {
				return ErrUnavailable
			}
		}
		var busy int
		if stepErr3 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_tasks WHERE owner_id=? AND device_id=? AND action>=6 AND session_id=? AND session_generation=? AND state IN ('queued','running','awaiting_confirmation','needs_user_action','unknown_outcome')`, c.Subject, in.DeviceId, sessionID, generation).Scan(&busy); stepErr3 != nil {
			return stepErr3
		}
		if busy > 0 {
			return ErrBusy
		}
		expiry := expiryBound(e.ExpiresAt.AsTime(), c.ExpiresAt.Time, mirror.ExpiresAt.AsTime())
		now := s.clock()
		approval, stepErr4 := metadata(s.auth.Authority(c))
		if stepErr4 != nil {
			return stepErr4
		}
		id := in.Operation.OperationId
		result = Task{ID: id, Owner: c.Subject, Device: in.DeviceId, ViewerDevice: c.DeviceID, CallerFamily: c.FamilyID, Conversation: in.ConversationId, Turn: in.TurnId, Generation: in.HistoryGeneration, Action: v1.ActionKind_ACTION_KIND_UI_OBSERVE, Session: sessionID, SessionGeneration: generation, Consent: PolicyRevision, Destination: "mobile.result:" + c.DeviceID, State: "queued", CreatedAt: now, ExpiresAt: expiry}
		if !s.nativeSessionLive(ctx, tx, result) {
			return ErrUnavailable
		}
		_, stepErr4 = tx.ExecContext(ctx, `INSERT INTO mobile_phone_tasks(id,owner_id,device_id,conversation_id,turn_id,history_generation,action,request_digest,caller_family_id,viewer_device_id,state,session_id,session_generation,created_at,expires_at,consent_revision,destination,approval_authority) VALUES(?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?)`, id, c.Subject, in.DeviceId, in.ConversationId, in.TurnId, in.HistoryGeneration, result.Action, requestDigest, c.FamilyID, c.DeviceID, sessionID, generation, now.UnixMilli(), expiry.UnixMilli(), PolicyRevision, result.Destination, approval)
		if stepErr4 != nil {
			return stepErr4
		}
		lease := expiryBound(now.Add(10*time.Second), expiry)
		if _, stepErr5 := tx.ExecContext(ctx, `INSERT INTO mobile_ui_tasks(task_id,manifest_digest,envelope_digest,lease_until,renewed_at,hard_until) VALUES(?,?,?,?,?,?)`, id, manifestDigest(manifest), digest, lease.UnixMilli(), now.UnixMilli(), expiry.UnixMilli()); stepErr5 != nil {
			return stepErr5
		}
		raw, stepErr4 := proto.Marshal(e)
		if stepErr4 != nil {
			return stepErr4
		}
		defer clear(raw)
		if stepErr11 := s.content.Put("ui-envelope:"+id, s.binding(result.Owner, result.Device, id, result.Destination, result.Consent, "command"), raw, expiry); stepErr11 != nil {
			return stepErr11
		}
		if stepErr12 := s.queueUI(ctx, tx, result, epoch, 0, "", &v1.UIAction{Kind: v1.ActionKind_ACTION_KIND_UI_OBSERVE}); stepErr12 != nil {
			return stepErr12
		}
		return taskEvent(ctx, tx, id)
	})
	return result, err
}
func (s *Store) queueUI(ctx context.Context, tx *sql.Tx, t Task, epoch uint64, step int, snapshot string, action *v1.UIAction) error {
	if step < 0 || step > 8 {
		return ErrInvalid
	}
	var seq uint64
	var lease int64
	if err := tx.QueryRowContext(ctx, `SELECT lease_until FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&lease); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `UPDATE mobile_device_streams SET server_seq=server_seq+1 WHERE owner_id=? AND device_id=? AND epoch=? RETURNING server_seq`, t.Owner, t.Device, epoch).Scan(&seq); err != nil {
		return err
	}
	e, err := s.envelope(t)
	if err != nil {
		return err
	}
	expiry := expiryBound(s.clock().Add(3*time.Second), time.UnixMilli(lease), t.ExpiresAt)
	commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(t.ID+":ui:"+strconv.Itoa(step+1))).String()
	command := &v1.Command{ClockId: s.clockID(), ProtocolVersion: 1, CommandId: commandID, TaskId: t.ID, DeviceId: t.Device, ServerSeq: seq, IssuedAt: timestamppb.Now(), ExpiresAt: timestamppb.New(expiry), SessionId: t.Session, SessionGeneration: t.SessionGeneration, CoordinatorEpoch: epoch, TaskStep: uint32(step + 1), SnapshotId: snapshot, Origin: &v1.TaskOrigin{ChannelType: "mobile", ChannelInstance: "mobile", BindingRevision: PolicyRevision, ConversationId: t.Conversation, TurnId: t.Turn}, Envelope: e, Action: &v1.Command_Ui{Ui: action}}
	digest, err := semantic(command)
	if err != nil {
		return err
	}
	command.PayloadDigestSha256 = digest
	body, err := proto.Marshal(command)
	if err != nil {
		return err
	}
	defer clear(body)
	if stepErr6 := s.content.Put(commandID, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "command"), body, expiry); stepErr6 != nil {
		return stepErr6
	}
	if _, stepErr7 := tx.ExecContext(ctx, `INSERT INTO mobile_phone_commands(id,task_id,owner_id,device_id,server_seq,digest,state,stream_epoch,coordinator_epoch,expires_at,payload_ref) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`, commandID, t.ID, t.Owner, t.Device, seq, digest, epoch, epoch, expiry.UnixMilli(), commandID); stepErr7 != nil {
		return stepErr7
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mobile_ui_commands VALUES(?,?,?)`, commandID, step, snapshot)
	return err
}

// RenewUI renews a still-live UI lease; expired generations never resurrect.
func (s *Store) RenewUI(ctx context.Context, identity Identity, epoch uint64, renew *v1.LeaseRenewal) (*v1.Lease, error) {
	return s.uiLease(ctx, identity, epoch, renew, true)
}

// UILease reads the current native lease without extending it during dispatch.
func (s *Store) UILease(ctx context.Context, identity Identity, epoch uint64, task *v1.LeaseRenewal) (*v1.Lease, error) {
	return s.uiLease(ctx, identity, epoch, task, false)
}

func (s *Store) uiLease(ctx context.Context, identity Identity, epoch uint64, renew *v1.LeaseRenewal, extend bool) (*v1.Lease, error) {
	if renew == nil {
		return nil, ErrInvalid
	}
	var result *v1.Lease
	err := s.with(ctx, identity, func(tx *sql.Tx, c *oauthstore.Claims) error {
		if scope(c, "phone:ui") != nil {
			return ErrUnavailable
		}
		if err := streamLive(ctx, tx, c, epoch); err != nil {
			return err
		}
		t, err := taskIn(ctx, tx, c.Subject, renew.TaskId)
		if err != nil {
			return err
		}
		if t.Device != c.DeviceID || t.Session != renew.SessionId || t.SessionGeneration != renew.SessionGeneration || !isUI(t.Action) || !s.nativeSessionLive(ctx, tx, t) || !s.originFamilyLive(ctx, tx, t) || s.chats.OriginAllowed(ctx, t.Owner, t.Conversation, t.Turn, t.Generation) != nil {
			return ErrUnavailable
		}
		var until, last, hard int64
		if tx.QueryRowContext(ctx, `SELECT lease_until,renewed_at,hard_until FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&until, &last, &hard) != nil || until <= s.clock().UnixMilli() || t.Cancelled || t.State != "queued" && t.State != "running" {
			return ErrUnavailable
		}
		expiry := time.UnixMilli(until)
		if extend && s.clock().Sub(time.UnixMilli(last)) >= 3*time.Second {
			expiry = expiryBound(s.clock().Add(10*time.Second), time.UnixMilli(hard), c.ExpiresAt.Time, t.ExpiresAt)
			if _, err := tx.ExecContext(ctx, `UPDATE mobile_ui_tasks SET lease_until=?,renewed_at=? WHERE task_id=?`, expiry.UnixMilli(), s.clock().UnixMilli(), t.ID); err != nil {
				return err
			}
		}
		result = &v1.Lease{ClockId: s.clockID(), TaskId: t.ID, SessionId: t.Session, SessionGeneration: t.SessionGeneration, ExpiresAt: timestamppb.New(expiry)}
		return nil
	})
	return result, err
}
func (s *Store) planningSnapshot(ctx context.Context, tx *sql.Tx, t Task) (*v1.UITree, error) {
	var ref string
	var until int64
	if tx.QueryRowContext(ctx, `SELECT snapshot_ref,lease_until FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&ref, &until) != nil || ref == "" || until <= s.clock().UnixMilli() {
		return nil, ErrUnavailable
	}
	body, _, err := s.content.Get(ref, s.binding(t.Owner, t.Device, t.ID, t.Destination, t.Consent, "ui_tree"))
	if err != nil {
		return nil, err
	}
	defer clear(body)
	observation := new(v1.Observation)
	if proto.Unmarshal(body, observation) != nil || s.clock().Sub(observation.CollectedAt.AsTime()) > 3*time.Second {
		return nil, ErrUnavailable
	}
	tree := observation.GetUiTree()
	if tree == nil || !tree.ExpiresAt.AsTime().After(s.clock()) {
		return nil, ErrUnavailable
	}
	return tree, nil
}
func (s *Store) claimPlan(ctx context.Context, tx *sql.Tx, t Task, snapshot string) error {
	tree, err := s.planningSnapshot(ctx, tx, t)
	if err != nil || tree.SnapshotId != snapshot {
		return ErrUnavailable
	}
	var count int
	if tx.QueryRowContext(ctx, `SELECT replans FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&count) != nil || count >= 2 {
		return ErrBusy
	}
	_, err = tx.ExecContext(ctx, `UPDATE mobile_ui_tasks SET replans=replans+1 WHERE task_id=?`, t.ID)
	return err
}
func (s *Store) applyPlan(ctx context.Context, tx *sql.Tx, t Task, snapshot, output string) error {
	var choice struct {
		SnapshotID string `json:"snapshot_id"`
		Step       int    `json:"step"`
	}
	dec := json.NewDecoder(bytes.NewBufferString(output))
	dec.DisallowUnknownFields()
	if dec.Decode(&choice) != nil || dec.Decode(new(any)) != io.EOF || choice.SnapshotID != snapshot {
		return ErrInvalid
	}
	current, snapshotErr := s.planningSnapshot(ctx, tx, t)
	if snapshotErr != nil || current.SnapshotId != snapshot {
		return ErrUnavailable
	}
	var step int
	if tx.QueryRowContext(ctx, `SELECT step FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&step) != nil || choice.Step != step {
		return ErrUnavailable
	}
	return s.advanceUI(ctx, tx, t)
}
func (s *Store) advanceUI(ctx context.Context, tx *sql.Tx, t Task) error {
	current, lookupErr := taskIn(ctx, tx, t.Owner, t.ID)
	if lookupErr != nil {
		return lookupErr
	}
	t = current
	if t.Cancelled || t.State != "running" || !s.nativeSessionLive(ctx, tx, t) || !s.originFamilyLive(ctx, tx, t) || s.chats.OriginAllowed(ctx, t.Owner, t.Conversation, t.Turn, t.Generation) != nil {
		return ErrUnavailable
	}
	var step int
	var expected []byte
	var epoch uint64
	if tx.QueryRowContext(ctx, `SELECT u.step,u.manifest_digest,d.epoch FROM mobile_ui_tasks u JOIN mobile_device_streams d ON d.owner_id=? AND d.device_id=? WHERE u.task_id=?`, t.Owner, t.Device, t.ID).Scan(&step, &expected, &epoch) != nil {
		return ErrUnavailable
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_phone_commands WHERE task_id=? AND state<>'succeeded'`, t.ID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrUnavailable
	}
	tree, err := s.planningSnapshot(ctx, tx, t)
	if err != nil {
		return err
	}
	e, err := s.envelope(t)
	if err != nil {
		return err
	}
	manifest, err := s.scenario(e.Scenario)
	if err != nil || !bytes.Equal(manifestDigest(manifest), expected) || !proto.Equal(tree.Scenario, e.Scenario) {
		return ErrUnavailable
	}
	var reused int
	if stepErr8 := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mobile_ui_commands u JOIN mobile_phone_commands c ON c.id=u.command_id WHERE c.task_id=? AND u.snapshot_id=?`, t.ID, tree.SnapshotId).Scan(&reused); stepErr8 != nil {
		return stepErr8
	}
	if reused > 0 {
		return ErrUnavailable
	}
	nodes := map[string]*v1.UINode{}
	for _, n := range tree.Nodes {
		if n.ResourceId != "" && nodes[n.ResourceId] != nil {
			return ErrInvalid
		}
		if n.ResourceId != "" {
			nodes[n.ResourceId] = n
		}
	}
	if step == len(manifest.Steps) {
		node := nodes[manifest.OutcomeResourceID]
		if node == nil || manifest.OutcomeTextField != "" && node.Text != field(e, manifest.OutcomeTextField) {
			return ErrUnavailable
		}
		if stepErr9 := taskTransitions(ctx, tx, `UPDATE mobile_phone_tasks SET state='succeeded',finished_at=? WHERE id=?`, s.clock().UnixMilli(), t.ID); stepErr9 != nil {
			return stepErr9
		}
		_, err = tx.ExecContext(ctx, `UPDATE mobile_ui_tasks SET snapshot_ref='' WHERE task_id=?`, t.ID)
		return err
	}
	if step < 0 || step > len(manifest.Steps) {
		return ErrInvalid
	}
	transition := manifest.Steps[step]
	node := nodes[transition.ResourceID]
	if node == nil || node.ClassName != transition.ClassName || node.Sensitive || node.Password {
		return ErrUnavailable
	}
	allowed := false
	for _, a := range node.Actions {
		if a == transition.Kind {
			allowed = true
		}
	}
	if !allowed {
		return ErrUnavailable
	}
	if transition.RecipientResourceID != "" {
		recipient := nodes[transition.RecipientResourceID]
		if recipient == nil || recipient.Text != e.Recipient {
			return ErrUnavailable
		}
	}
	action := &v1.UIAction{Kind: transition.Kind, NodeHandle: node.Handle, Text: field(e, transition.TextField)}
	if transition.Kind == v1.ActionKind_ACTION_KIND_UI_SCROLL {
		action.ScrollDirection = 1
	}
	if stepErr10 := s.queueUI(ctx, tx, t, epoch, step+1, tree.SnapshotId, action); stepErr10 != nil {
		return stepErr10
	}
	_, err = tx.ExecContext(ctx, `UPDATE mobile_ui_tasks SET step=step+1 WHERE task_id=?`, t.ID)
	return err
}
func validateTree(tree *v1.UITree) error {
	if tree == nil || !validID(tree.SnapshotId) || tree.WindowId == 0 || !tree.ApplicationWindow || !tree.Unlocked || !tree.Foreground || !tree.SafetyControlVisible || len(tree.Nodes) > 256 || tree.ExpiresAt == nil || tree.ExpiresAt.CheckValid() != nil || !tree.ExpiresAt.AsTime().After(time.Now()) || tree.ExpiresAt.AsTime().After(time.Now().Add(3*time.Second)) {
		return ErrInvalid
	}
	handles := map[string]bool{}
	for _, n := range tree.Nodes {
		if n == nil || !validID(n.Handle) || handles[n.Handle] || len(n.Text) > 8192 || len(n.Description) > 1024 || len(n.ResourceId) > 256 || len(n.ClassName) > 256 || n.Bounds == nil || n.Bounds.Right < n.Bounds.Left || n.Bounds.Bottom < n.Bounds.Top || n.Sensitive || n.Password {
			return ErrInvalid
		}
		if len(n.Actions) > 6 {
			return ErrInvalid
		}
		seen := map[v1.ActionKind]bool{}
		for _, kind := range n.Actions {
			if !isUI(kind) || seen[kind] {
				return ErrInvalid
			}
			seen[kind] = true
		}
		handles[n.Handle] = true
	}
	return nil
}
func (s *Store) uiDispatch(ctx context.Context, tx *sql.Tx, t Task, command *v1.Command) bool {
	if command.GetUi() == nil {
		return false
	}
	var until int64
	if tx.QueryRowContext(ctx, `SELECT lease_until FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&until) != nil || until <= s.clock().UnixMilli() || !command.ExpiresAt.AsTime().After(s.clock()) {
		return false
	}
	var raw []byte
	if tx.QueryRowContext(ctx, `SELECT metadata FROM mobile_native_sessions WHERE owner_id=? AND device_id=? AND id=? AND generation=?`, t.Owner, t.Device, t.Session, t.SessionGeneration).Scan(&raw) != nil {
		return false
	}
	mirror := new(v1.SessionMirror)
	if proto.Unmarshal(raw, mirror) != nil || !mirror.ScreenContentAllowed {
		return false
	}
	if command.GetUi().Kind != v1.ActionKind_ACTION_KIND_UI_OBSERVE {
		tree, err := s.planningSnapshot(ctx, tx, t)
		if err != nil || tree.SnapshotId != command.SnapshotId {
			return false
		}
	}
	for _, a := range mirror.AllowedActions {
		if a == command.GetUi().Kind {
			return true
		}
	}
	return false
}

func (s *Store) uiLeaseLive(ctx context.Context, tx *sql.Tx, t Task) bool {
	if !isUI(t.Action) {
		return true
	}
	if !s.SupportsUI() {
		return false
	}
	var until int64
	return tx.QueryRowContext(ctx, `SELECT lease_until FROM mobile_ui_tasks WHERE task_id=?`, t.ID).Scan(&until) == nil && until > s.clock().UnixMilli()
}
