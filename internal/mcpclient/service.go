package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
)

// Service owns owner-scoped policy, durable executions and runtime admission.
type Service struct {
	mu                sync.Mutex
	workers           sync.WaitGroup
	repo              Repository
	vault             Vault
	protocol          Protocol
	root              context.Context
	cancel            context.CancelFunc
	lock              *flock.Flock
	broken, closed    bool
	policy            Policy
	deploymentOrigins []string
	deploymentEnabled bool
	principal         func(context.Context, string) bool
	connections       map[string]*storedConnection
	executions        map[string]*storedExecution
	plans             map[string]*Plan
	active            map[string]context.CancelFunc
	probes            map[string]context.CancelFunc
	refreshing        map[string]chan struct{}
	authWork          map[string]map[string]context.CancelFunc
	revisions         map[string]int64
	artifacts         map[string]artifactInfo
	oauthExpiry       map[string]artifactInfo
}

type artifactInfo struct {
	owner   string
	bytes   int
	expires time.Time
}

// New opens the encrypted catalog and acquires exclusive control-plane ownership.
func New(o Options) (*Service, error) {
	if o.Repository == nil || o.Vault == nil || !o.Vault.EncryptionEnabled() || o.Protocol == nil || o.LockPath == "" {
		return nil, ErrUnavailable
	}
	if o.Root == nil {
		o.Root = context.Background()
	}
	root, cancel := context.WithCancel(o.Root)
	s := &Service{artifacts: map[string]artifactInfo{}, oauthExpiry: map[string]artifactInfo{}, deploymentEnabled: o.Enabled, cancel: cancel, repo: o.Repository, vault: o.Vault, protocol: o.Protocol, root: root, lock: flock.New(o.LockPath), principal: o.PrincipalChecker, deploymentOrigins: append([]string(nil), o.AllowedOrigins...), connections: map[string]*storedConnection{}, executions: map[string]*storedExecution{}, plans: map[string]*Plan{}, active: map[string]context.CancelFunc{}, probes: map[string]context.CancelFunc{}, refreshing: map[string]chan struct{}{}, revisions: map[string]int64{}, policy: Policy{Enabled: o.Enabled, AllowedOrigins: append([]string(nil), o.AllowedOrigins...), Revision: 1}}
	ok, err := s.lock.TryLock()
	if err != nil || !ok {
		cancel()
		return nil, ErrUnavailable
	}
	fail := func() (*Service, error) {
		cancel()
		if unlockErr := s.lock.Unlock(); unlockErr != nil {
			slog.Warn("MCP startup lock cleanup incomplete")
		}
		return nil, ErrUnavailable
	}
	if runtime, ok := s.protocol.(interface{ SetRoot(context.Context) }); ok {
		runtime.SetRoot(root)
	}
	rows, err := s.repo.LoadMCPRecords(o.Root)
	if err != nil {
		return fail()
	}
	for _, r := range rows {
		plain, e := s.vault.OpenPrivate(r.Payload)
		if e != nil {
			return fail()
		}
		s.revisions[r.ID] = r.Revision
		switch r.Kind {
		case "connection":
			var c storedConnection
			if json.Unmarshal([]byte(plain), &c) != nil || c.Owner != r.OwnerID || c.ID != r.ID {
				return fail()
			}
			c.OwnerID = c.Owner
			c.Token = c.Secret
			c.Headers = c.SecretHeaders
			if c.Desired == "enabled" {
				c.Health = "stopped"
			}
			s.connections[c.ID] = &c
		case "execution":
			var x storedExecution
			if json.Unmarshal([]byte(plain), &x) != nil || x.OwnerID != r.OwnerID || x.ID != r.ID {
				return fail()
			}
			x.Owner = x.OwnerID
			s.executions[x.ID] = &x
		case "artifact":
			var a Artifact
			if json.Unmarshal([]byte(plain), &a) != nil || a.Owner != r.OwnerID {
				return fail()
			}
			s.artifacts[a.ID] = artifactInfo{a.Owner, len(a.Payload), a.ExpiresAt}
		case "oauth":
			var flow OAuthFlow
			if json.Unmarshal([]byte(plain), &flow) != nil || flow.Owner != r.OwnerID {
				return fail()
			}
			s.oauthExpiry[r.ID] = artifactInfo{owner: flow.Owner, expires: flow.ExpiresAt}
		case "policy":
			if json.Unmarshal([]byte(plain), &s.policy) != nil {
				return fail()
			}
		}
	}
	// Deployment restrictions always win over saved state.
	s.policy.Enabled = s.policy.Enabled && o.Enabled
	s.policy.AllowedOrigins = append([]string(nil), o.AllowedOrigins...)
	for _, x := range s.executions {
		if x.State == "running" || x.State == "dispatching" || x.State == "cancel_requested" {
			x.State = "unknown_outcome"
			x.SideEffectState = "unknown"
			x.FinishedAt = time.Now()
			if s.saveLocked(x.Owner, x.ID, "execution", x, "execution_state") != nil {
				return fail()
			}
		}
	}
	s.workers.Add(1)
	go s.sweep()
	return s, nil
}
func (s *Service) saveLocked(owner, id, kind string, v any, actions ...string) error {
	b, err := json.Marshal(v)
	if err != nil {
		s.broken = true
		return ErrUnavailable
	}
	sealed, err := s.vault.SealPrivate(string(b))
	if err != nil {
		s.broken = true
		return ErrUnavailable
	}
	old := s.revisions[id]
	r := &domain.MCPRecord{ID: id, OwnerID: owner, Kind: kind, Revision: old + 1, Payload: sealed, ActorID: owner}
	r.Action = kind + "_updated"
	if len(actions) > 0 {
		r.Action = actions[0]
	}
	switch value := v.(type) {
	case *storedConnection:
		r.State = value.Desired + ":" + value.Health
	case *storedExecution:
		r.State = value.State
	case Policy:
		if value.Enabled {
			r.State = "enabled"
		} else {
			r.State = "disabled"
		}
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(s.root), 5*time.Second)
	defer cancel()
	if policy, ok := v.(Policy); ok && policy.UpdatedBy != "" {
		r.ActorID = policy.UpdatedBy
	}
	if s.repo.SaveMCPRecord(saveCtx, r, old) != nil {
		s.broken = true
		for _, c := range s.active {
			c()
		}
		return ErrUnavailable
	}
	s.revisions[id] = r.Revision
	if f, ok := v.(OAuthFlow); ok {
		s.oauthExpiry[id] = artifactInfo{owner: f.Owner, expires: f.ExpiresAt}
	}
	if a, ok := v.(Artifact); ok {
		s.artifacts[id] = artifactInfo{a.Owner, len(a.Payload), a.ExpiresAt}
	}
	return nil
}
func validScope(sc Scope) bool {
	return sc.OwnerID != "" && sc.Private && (sc.CallerID == "dashboard" || sc.CallerID == "main" || sc.CallerID == "native")
}
func (s *Service) admit(ctx context.Context, sc Scope) error {
	if !validScope(sc) {
		return ErrDenied
	}
	if s.principal != nil && !s.principal(ctx, sc.OwnerID) {
		return ErrDenied
	}
	return nil
}

// networkWork fences shutdown against OAuth work as well as protocol calls.
func (s *Service) networkWork(parent context.Context, connection string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.broken || s.root.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(s.root, time.Minute)
	stop := context.AfterFunc(parent, cancel)
	if s.authWork == nil {
		s.authWork = map[string]map[string]context.CancelFunc{}
	}
	if s.authWork[connection] == nil {
		s.authWork[connection] = map[string]context.CancelFunc{}
	}
	id := uuid.NewString()
	s.authWork[connection][id] = cancel
	s.workers.Add(1)
	return ctx, func() {
		stop()
		cancel()
		s.mu.Lock()
		delete(s.authWork[connection], id)
		if len(s.authWork[connection]) == 0 {
			delete(s.authWork, connection)
		}
		s.mu.Unlock()
		s.workers.Done()
	}, nil
}
func (s *Service) getLocked(sc Scope, id string) (*storedConnection, error) {
	if !validScope(sc) || s.closed || s.broken {
		return nil, ErrDenied
	}
	c := s.connections[id]
	if c == nil || c.Owner != sc.OwnerID || c.Desired == "deleted" {
		return nil, ErrDenied
	}
	return c, nil
}
func runtimeConnection(c *storedConnection) Connection {
	v := c.Connection
	v.Headers = map[string]string{}
	for k, h := range c.Headers {
		v.Headers[k] = h
	}
	v.Items = nil
	return v
}

func safeConnection(c *storedConnection, detail bool) Connection {
	v := c.Connection
	v.OwnerID = ""
	v.Token = ""
	v.Headers = nil
	v.Items = append([]Item(nil), c.Items...)
	for i := range v.Items {
		v.Items[i].Descriptor = append(json.RawMessage(nil), c.Items[i].Descriptor...)
	}
	if !detail {
		v.Endpoint = safeOrigin(c.Endpoint)
		v.Items = nil
	}
	return v
}

// Snapshot returns only the authenticated owner connection and operation metadata.
func (s *Service) Snapshot(ctx context.Context, sc Scope) (Snapshot, error) {
	if s.admit(ctx, sc) != nil {
		return Snapshot{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{Enabled: s.policy.Enabled && !s.broken && !s.closed, Connections: []Connection{}, Executions: []Execution{}}
	for _, c := range s.connections {
		if c.Owner == sc.OwnerID && c.Desired != "deleted" {
			out.Connections = append(out.Connections, safeConnection(c, false))
		}
	}
	for _, x := range s.executions {
		if x.Owner == sc.OwnerID {
			v := x.Execution
			v.Text = ""
			out.Executions = append(out.Executions, v)
		}
	}
	sort.Slice(out.Connections, func(i, j int) bool { return out.Connections[i].Label < out.Connections[j].Label })
	sort.Slice(out.Executions, func(i, j int) bool { return out.Executions[i].CreatedAt.After(out.Executions[j].CreatedAt) })
	if len(out.Executions) > 100 {
		out.Executions = out.Executions[:100]
	}
	return out, nil
}

// Connection describes a private connection without exporting authentication material.
func (s *Service) Connection(ctx context.Context, sc Scope, id string) (Connection, error) {
	if s.admit(ctx, sc) != nil {
		return Connection{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		return Connection{}, e
	}
	return safeConnection(c, sc.CallerID == "dashboard"), nil
}

// Create saves an owner draft without contacting an external server.
func (s *Service) Create(ctx context.Context, sc Scope, d Draft) (Connection, error) {
	if s.admit(ctx, sc) != nil {
		return Connection{}, ErrDenied
	}
	d.Label = strings.TrimSpace(d.Label)
	if d.Label == "" || len(d.Label) > 120 || (d.Domain != "personal" && d.Domain != "work" && d.Domain != "other") {
		return Connection{}, ErrInvalid
	}
	if d.Transport == "" {
		d.Transport = "http"
	}
	if d.Transport != "http" && d.Transport != "stdio" {
		return Connection{}, ErrInvalid
	}
	if d.Transport == "http" {
		if _, e := validateEndpoint(d.Endpoint); e != nil {
			return Connection{}, e
		}
	} else if d.Profile == "" || d.Endpoint != "" {
		return Connection{}, ErrInvalid
	}
	if d.ResultMode == "" {
		d.ResultMode = "private"
	}
	if d.ResultMode != "private" && d.ResultMode != "model" {
		return Connection{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	if s.closed || s.broken {
		return Connection{}, ErrUnavailable
	}
	for _, c := range s.connections {
		if c.Owner == sc.OwnerID && c.Desired != "deleted" {
			n++
		}
	}
	if n >= 10 {
		return Connection{}, ErrDenied
	}
	c := &storedConnection{Owner: sc.OwnerID, Connection: Connection{ID: uuid.NewString(), OwnerID: sc.OwnerID, Label: d.Label, Domain: d.Domain, Endpoint: d.Endpoint, Transport: d.Transport, Profile: d.Profile, Desired: "draft", Health: "stopped", AuthMode: "none", AuthState: "none", ResultMode: d.ResultMode, Revision: 1, Generation: 1, Items: []Item{}}}
	if requestErr := s.saveLocked(sc.OwnerID, c.ID, "connection", c, "connection_created"); requestErr != nil {
		return Connection{}, requestErr
	}
	s.connections[c.ID] = c
	return safeConnection(c, sc.CallerID == "dashboard"), nil
}

// Update is a settings-only operation. Changing destination/domain/transport
// revokes the old authentication and catalog rather than reusing authority.
func (s *Service) Update(ctx context.Context, sc Scope, id string, revision int64, d Draft) (Connection, error) {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return Connection{}, ErrDenied
	}
	if strings.TrimSpace(d.Label) == "" || len(d.Label) > 120 || (d.Domain != "personal" && d.Domain != "work" && d.Domain != "other") || (d.ResultMode != "private" && d.ResultMode != "model") {
		return Connection{}, ErrInvalid
	}
	if d.Transport == "http" {
		if _, e := validateEndpoint(d.Endpoint); e != nil {
			return Connection{}, e
		}
	} else if d.Transport != "stdio" || d.Profile == "" || d.Endpoint != "" {
		return Connection{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		return Connection{}, e
	}
	if c.Revision != revision {
		return Connection{}, ErrConflict
	}
	changed := c.Endpoint != d.Endpoint || c.Transport != d.Transport || c.Profile != d.Profile || c.Domain != d.Domain
	s.stopLocked(id)
	c.Label = strings.TrimSpace(d.Label)
	c.Domain = d.Domain
	c.Transport = d.Transport
	c.Endpoint = d.Endpoint
	c.Profile = d.Profile
	c.ResultMode = d.ResultMode
	c.Generation++
	c.Revision++
	c.Desired = "disabled"
	c.Health = "stopped"
	if changed {
		c.Token = ""
		c.Secret = ""
		c.SecretHeaders = nil
		c.Headers = nil
		c.OAuth = nil
		c.AuthMode = "none"
		c.AuthState = "none"
		c.Items = []Item{}
	}
	if requestErr := s.saveLocked(sc.OwnerID, id, "connection", c, "settings_updated"); requestErr != nil {
		return Connection{}, requestErr
	}
	return safeConnection(c, true), nil
}

// SetCredentials never accepts credentials from model-facing adapters.
func (s *Service) SetCredentials(ctx context.Context, sc Scope, id string, revision int64, mode, token string, headers map[string]string) error {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return ErrDenied
	}
	if mode != "none" && mode != "bearer" && mode != "headers" {
		return ErrInvalid
	}
	if len(token) > 16384 || (mode == "bearer" && strings.TrimSpace(token) == "") || strings.ContainsAny(token, "\r\n") {
		return ErrInvalid
	}
	if requestErr := validateHeaders(headers); requestErr != nil {
		return requestErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		return e
	}
	if c.Revision != revision {
		return ErrConflict
	}
	if c.Transport == "stdio" && mode == "headers" {
		return ErrInvalid
	}
	switch mode {
	case "none":
		token = ""
		headers = nil
	case "bearer":
		headers = nil
	case "headers":
		token = ""
	}
	copiedHeaders := map[string]string{}
	for k, v := range headers {
		copiedHeaders[k] = v
	}
	headers = copiedHeaders
	s.stopLocked(id)
	c.Token = token
	c.Secret = token
	c.Headers = headers
	c.SecretHeaders = headers
	c.OAuth = nil
	c.AuthMode = mode
	c.AuthState = "valid"
	if mode == "none" {
		c.AuthState = "none"
	}
	c.Desired = "disabled"
	c.Generation++
	c.Revision++
	return s.saveLocked(sc.OwnerID, id, "connection", c, "credentials_updated")
}
func (s *Service) stopLocked(id string) {
	for _, cancel := range s.authWork[id] {
		cancel()
	}
	if c := s.connections[id]; c != nil {
		if runtime, ok := s.protocol.(interface{ Invalidate(string, string) }); ok {
			runtime.Invalidate(c.Owner, id)
		}
	}
	if cancel := s.probes[id]; cancel != nil {
		cancel()
		delete(s.probes, id)
	}
	for xid, cancel := range s.active {
		if x := s.executions[xid]; x != nil && x.ConnectionID == id {
			cancel()
		}
	}
	for pid, p := range s.plans {
		if p.Call.ConnectionID == id {
			delete(s.plans, pid)
		}
	}
}

// Action defines the action MCP contract.
func (s *Service) Action(ctx context.Context, sc Scope, id, action string, revision int64) (Connection, error) {
	if s.admit(ctx, sc) != nil {
		return Connection{}, ErrDenied
	}
	if action == "connect" || action == "reconnect" || action == "check" || action == "refresh_catalog" {
		var err error
		revision, err = s.refreshAuth(ctx, sc, id, revision, true)
		if err != nil {
			return Connection{}, err
		}
	}
	s.mu.Lock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		s.mu.Unlock()
		return Connection{}, e
	}
	if c.Revision != revision {
		s.mu.Unlock()
		return Connection{}, ErrConflict
	}
	switch action {
	case "disconnect", "delete", "revoke_auth":
		s.stopLocked(id)
		c.Generation++
		c.Revision++
		c.Desired = "disabled"
		c.Health = "stopped"
		if action != "disconnect" {
			c.Secret = ""
			c.Token = ""
			c.SecretHeaders = nil
			c.Headers = nil
			c.OAuth = nil
			c.AuthState = "revoked"
			for i := range c.Items {
				c.Items[i].Allowed = false
			}
		}
		if action == "delete" {
			c.Desired = "deleted"
		}
		e = s.saveLocked(sc.OwnerID, id, "connection", c, "connection_"+action)
		v := safeConnection(c, true)
		s.mu.Unlock()
		return v, e
	case "connect", "reconnect", "check", "refresh_catalog":
		if action == "check" || action == "refresh_catalog" {
			for xid := range s.active {
				if s.executions[xid].ConnectionID == id {
					s.mu.Unlock()
					return Connection{}, ErrBusy
				}
			}
		}
	default:
		s.mu.Unlock()
		return Connection{}, ErrInvalid
	}
	if !s.policy.Enabled || s.broken || s.closed {
		s.mu.Unlock()
		return Connection{}, ErrUnavailable
	}
	if c.Transport == "http" && !s.originAllowedLocked(c.Endpoint) {
		s.mu.Unlock()
		return Connection{}, ErrDenied
	}
	s.stopLocked(id)
	c.Generation++
	c.Revision++
	if action == "connect" || action == "reconnect" {
		c.Desired = "enabled"
	}
	c.Health = "starting"
	generation := c.Generation
	if requestErr := s.saveLocked(sc.OwnerID, id, "connection", c, "connection_"+action); requestErr != nil {
		s.mu.Unlock()
		return Connection{}, requestErr
	}
	copyC := runtimeConnection(c)
	work, cancel := context.WithTimeout(s.root, 30*time.Second)
	stopParent := context.AfterFunc(ctx, cancel)
	s.workers.Add(1)
	s.probes[id] = cancel
	s.mu.Unlock()
	defer s.workers.Done()
	defer stopParent()
	defer cancel()
	items, err := s.protocol.Discover(work, copyC)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e = s.getLocked(sc, id)
	if e != nil {
		return Connection{}, e
	}
	if c.Generation != generation {
		return Connection{}, ErrConflict
	}
	delete(s.probes, id)
	c.CheckedAt = time.Now()
	c.Revision++
	if err != nil {
		c.Health = "degraded"
		c.ErrorCode = "discovery_failed"
	} else {
		c.Health = "ready"
		c.ErrorCode = ""
		for i := range items {
			for oldIndex := range c.Items {
				old := &c.Items[oldIndex]
				if old.ID == items[i].ID && old.Revision == items[i].Revision {
					items[i].Allowed = old.Allowed
					items[i].Effect = old.Effect
				}
			}
		}
		c.Items = items
	}
	if requestErr := s.saveLocked(sc.OwnerID, id, "connection", c, "connection_"+action); requestErr != nil {
		return Connection{}, requestErr
	}
	return safeConnection(c, sc.CallerID == "dashboard"), nil
}
func (s *Service) originAllowedLocked(endpoint string) bool {
	origin := safeOrigin(endpoint)
	found := func(values []string) bool {
		for _, v := range values {
			if v == origin {
				return true
			}
		}
		return false
	}
	return found(s.deploymentOrigins) && found(s.policy.AllowedOrigins)
}

// Review binds an owner access decision to an exact descriptor hash.
func (s *Service) Review(ctx context.Context, sc Scope, id string, revision int64, r Review) error {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return ErrDenied
	}
	if r.Effect != "read" && r.Effect != "write" && r.Effect != "unknown" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		return e
	}
	if c.Revision != revision {
		return ErrConflict
	}
	for i := range c.Items {
		it := &c.Items[i]
		if it.ID == r.ItemID && it.Revision == r.Revision {
			if it.ErrorCode != "" && r.Allowed {
				return ErrInvalid
			}
			it.Allowed = r.Allowed
			it.Effect = r.Effect
			c.Revision++
			s.stopLocked(id)
			return s.saveLocked(sc.OwnerID, id, "connection", c, "capability_reviewed")
		}
	}
	return ErrDenied
}

// Catalog returns owner-permitted descriptors without injecting them into system prompts.
func (s *Service) Catalog(ctx context.Context, sc Scope, id, kind string) ([]Item, error) {
	if s.admit(ctx, sc) != nil {
		return nil, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, id)
	if e != nil {
		return nil, e
	}
	out := []Item{}
	for itemIndex := range c.Items {
		it := c.Items[itemIndex]
		if (kind == "" || kind == it.Kind || kind == "resource" && it.Kind == "resource_template") && (sc.CallerID == "dashboard" || it.Allowed) {
			clone := it
			clone.Descriptor = append(json.RawMessage(nil), it.Descriptor...)
			out = append(out, clone)
		}
	}
	return out, nil
}

// Prepare validates and freezes a bounded operation before any remote execution.
func (s *Service) Prepare(ctx context.Context, sc Scope, in Call) (Plan, error) {
	if s.admit(ctx, sc) != nil {
		return Plan{}, ErrDenied
	}
	if err := s.RefreshAuth(ctx, sc, in.ConnectionID); err != nil {
		return Plan{}, err
	}
	if len(in.Arguments) == 0 {
		in.Arguments = json.RawMessage(`{}`)
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 60
	}
	if in.TimeoutSeconds < 1 || in.TimeoutSeconds > 300 || validJSON(in.Arguments, 64<<10) != nil {
		return Plan{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, e := s.getLocked(sc, in.ConnectionID)
	if e != nil {
		return Plan{}, e
	}
	// Invoke revalidates the complete catalog in the dispatch session. An idle
	// connection does not need a manual check merely because five minutes passed.
	if !s.policy.Enabled || c.Desired != "enabled" || (c.AuthMode != "none" && c.AuthState != "valid") || c.Health != "ready" {
		return Plan{}, ErrUnavailable
	}
	in.Arguments = append(json.RawMessage(nil), in.Arguments...)
	var item *Item
	for i := range c.Items {
		if c.Items[i].ID == in.ItemID {
			it := c.Items[i]
			item = &it
			break
		}
	}
	if item == nil || !item.Allowed || item.Revision != in.DescriptorRevision || item.ErrorCode != "" {
		return Plan{}, ErrDenied
	}
	if validateArguments(*item, in.Arguments) != nil {
		return Plan{}, ErrInvalid
	}
	p := &Plan{ID: uuid.NewString(), Owner: sc.OwnerID, Caller: sc.CallerID, Conversation: sc.ConversationID, Call: in, Generation: c.Generation, ConnectionRevision: c.Revision, ExpiresAt: time.Now().Add(5 * time.Minute), RequiresConfirmation: true}
	// Even read tools can export private text in arguments: baseline exact consent
	// deliberately avoids claiming arbitrary-text DLP or unlimited read grants.
	destination := safeOrigin(c.Endpoint)
	if c.Transport == "stdio" {
		destination = "worker profile: " + c.Profile
	}
	p.Preview = fmt.Sprintf("MCP: %s\n%s · %s\n%s: %s\nArguments must be reviewed in private connection settings. Result: %s. Timeout: %ds.", c.Label, c.Domain, destination, item.Kind, item.Name, c.ResultMode, in.TimeoutSeconds)
	h, hashErr := json.Marshal(struct {
		Call                         Call
		Owner, Caller, Conversation  string
		Generation, Revision, Policy int64
	}{in, sc.OwnerID, sc.CallerID, sc.ConversationID, c.Generation, c.Revision, s.policy.Revision})
	if hashErr != nil {
		return Plan{}, ErrInvalid
	}
	sum := sha256.Sum256(h)
	p.Hash = hex.EncodeToString(sum[:])
	if len(s.plans) > 1000 {
		for id, v := range s.plans {
			if time.Now().After(v.ExpiresAt) {
				delete(s.plans, id)
			}
		}
	}
	if len(s.plans) > 1000 {
		return Plan{}, ErrUnavailable
	}
	s.plans[p.ID] = p
	return *p, nil
}

// Approve records a verified owner decision for an exact immutable plan.
func (s *Service) Approve(ctx context.Context, sc Scope, id, hash string, approved bool) error {
	if s.admit(ctx, sc) != nil {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.plans[id]
	if s.closed || s.broken {
		return ErrUnavailable
	}
	if p == nil || !planScope(p, sc) || p.Hash != hash || p.Consumed || time.Now().After(p.ExpiresAt) {
		return ErrConflict
	}
	if !approved {
		delete(s.plans, id)
		return nil
	}
	p.Approved = true
	return nil
}

// Dashboard is the explicit owner approval bridge for chat/native plans. Other
// adapters can only approve or execute the conversation that created the plan.
func planScope(p *Plan, sc Scope) bool {
	return p.Owner == sc.OwnerID && (sc.CallerID == "dashboard" || p.Caller == sc.CallerID && p.Conversation == sc.ConversationID)
}

// Plans lists unconsumed plans for the authenticated owner settings screen.
func (s *Service) Plans(ctx context.Context, sc Scope) ([]Plan, error) {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return nil, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Plan{}
	for _, p := range s.plans {
		if p.Owner == sc.OwnerID && !p.Consumed && time.Now().Before(p.ExpiresAt) {
			out = append(out, *p)
		}
	}
	return out, nil
}

// Execute journals and dispatches one approved operation without automatic replay.
func (s *Service) Execute(ctx context.Context, sc Scope, id string) (Execution, error) {
	return s.execute(ctx, sc, id, nil)
}

// Start admits and journals synchronously before acknowledging asynchronous work.
func (s *Service) Start(ctx context.Context, sc Scope, id string) error {
	admission := make(chan error, 1)
	go func() {
		var once sync.Once
		notify := func(err error) { once.Do(func() { admission <- err }) }
		_, err := s.execute(ctx, sc, id, func() { notify(nil) })
		notify(err)
	}()
	return <-admission
}

func (s *Service) execute(ctx context.Context, sc Scope, id string, admitted func()) (Execution, error) {
	if s.admit(ctx, sc) != nil {
		return Execution{}, ErrDenied
	}
	s.mu.Lock()
	if old := s.executions[id]; old != nil {
		v := old.Execution
		v.Text = ""
		s.mu.Unlock()
		if old.Owner != sc.OwnerID {
			return Execution{}, ErrDenied
		}
		return v, nil
	}
	p := s.plans[id]
	if p == nil || !planScope(p, sc) || p.Consumed || !p.Approved || time.Now().After(p.ExpiresAt) {
		s.mu.Unlock()
		return Execution{}, ErrApproval
	}
	c, e := s.getLocked(sc, p.Call.ConnectionID)
	if e != nil {
		s.mu.Unlock()
		return Execution{}, e
	}
	if !s.policy.Enabled || c.Generation != p.Generation || c.Revision != p.ConnectionRevision || c.Desired != "enabled" {
		s.mu.Unlock()
		return Execution{}, ErrConflict
	}
	if len(s.active) >= 16 {
		s.mu.Unlock()
		return Execution{}, ErrUnavailable
	}
	count := 0
	connectionCount := 0
	for xid := range s.active {
		if s.executions[xid].ConnectionID == c.ID {
			connectionCount++
		}
		if s.executions[xid].Owner == sc.OwnerID {
			count++
		}
	}
	if count >= 4 || connectionCount >= 2 {
		s.mu.Unlock()
		return Execution{}, ErrUnavailable
	}
	var item Item
	for itemIndex := range c.Items {
		it := c.Items[itemIndex]
		if it.ID == p.Call.ItemID {
			item = it
		}
	}
	if !item.Allowed || item.Revision != p.Call.DescriptorRevision {
		s.mu.Unlock()
		return Execution{}, ErrConflict
	}
	x := &storedExecution{OwnerID: sc.OwnerID, Execution: Execution{ID: id, Owner: sc.OwnerID, ConnectionID: c.ID, ItemID: item.ID, PlanHash: p.Hash, State: "running", SideEffectState: "not_started", CreatedAt: time.Now()}}
	if requestErr := s.saveLocked(sc.OwnerID, id, "execution", x, "execution_state"); requestErr != nil {
		s.mu.Unlock()
		return Execution{}, requestErr
	}
	s.executions[id] = x
	p.Consumed = true
	work, cancel := context.WithTimeout(s.root, time.Duration(p.Call.TimeoutSeconds)*time.Second)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	s.active[id] = cancel
	s.workers.Add(1)
	if admitted != nil {
		admitted()
	}
	defer s.workers.Done()
	copyC := runtimeConnection(c)
	call := p.Call
	gen := p.Generation
	s.mu.Unlock()
	before := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		current, er := s.getLocked(sc, copyC.ID)
		if er != nil || !s.policy.Enabled || current.Generation != gen || current.Revision != p.ConnectionRevision || current.Desired != "enabled" || work.Err() != nil {
			return ErrDenied
		}
		x.State = "dispatching"
		x.SideEffectState = "possible"
		return s.saveLocked(sc.OwnerID, id, "execution", x, "execution_state")
	}
	raw, toolError, err := s.protocol.Invoke(work, copyC, item, call.Arguments, before)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
	x.FinishedAt = time.Now()
	current := s.connections[copyC.ID]
	revoked := current == nil || current.Generation != gen || !s.policy.Enabled || work.Err() != nil
	if err != nil || revoked {
		if x.SideEffectState == "not_started" {
			x.State = "failed"
			x.ErrorCode = "connection_failed"
		} else {
			x.State = "unknown_outcome"
			x.SideEffectState = "unknown"
			x.ErrorCode = "result_unverified"
		}
	} else {
		x.State = "succeeded"
		x.SideEffectState = "provider_reported_complete"
		if toolError {
			x.State = "failed"
			x.ErrorCode = "tool_error"
			x.SideEffectState = "possible"
		}
		if len(raw) > 32<<20 {
			x.State = "result_unavailable"
			x.ErrorCode = "result_too_large"
		} else {
			bytesUsed := 0
			for _, meta := range s.artifacts {
				if meta.owner == sc.OwnerID {
					bytesUsed += meta.bytes
				}
			}
			if bytesUsed+len(raw) > 100<<20 {
				x.State = "result_unavailable"
				x.ErrorCode = "artifact_quota"
				if requestErr := s.saveLocked(sc.OwnerID, id, "execution", x, "execution_state"); requestErr != nil {
					return Execution{}, requestErr
				}
				return x.Execution, nil
			}
			a := Artifact{ID: uuid.NewString(), Owner: sc.OwnerID, ExecutionID: id, Payload: raw, ExpiresAt: time.Now().Add(24 * time.Hour)}
			if requestErr := s.saveLocked(sc.OwnerID, a.ID, "artifact", a); requestErr != nil {
				return Execution{}, requestErr
			}
			x.ArtifactID = a.ID
			if copyC.ResultMode == "model" {
				x.Text, x.Truncated = project(raw)
			}
		}
	}
	result := x.Execution
	x.Text = ""
	if requestErr := s.saveLocked(sc.OwnerID, id, "execution", x, "execution_state"); requestErr != nil {
		return Execution{}, requestErr
	}
	return result, nil
}

// Cancel requests cancellation without claiming that remote side effects stopped.
func (s *Service) Cancel(ctx context.Context, sc Scope, id string) error {
	if s.admit(ctx, sc) != nil {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.executions[id]
	if x == nil || x.Owner != sc.OwnerID {
		return ErrDenied
	}
	if f := s.active[id]; f != nil {
		f()
		x.State = "cancel_requested"
		return s.saveLocked(sc.OwnerID, id, "execution", x, "execution_state")
	}
	return nil
}

// Artifact represents private result bytes and their expiration and access metadata.
func (s *Service) Artifact(ctx context.Context, sc Scope, id string) (Artifact, error) {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return Artifact{}, ErrDenied
	}
	r, e := s.repo.GetMCPRecord(ctx, sc.OwnerID, id, "artifact")
	if e != nil {
		return Artifact{}, ErrDenied
	}
	plain, e := s.vault.OpenPrivate(r.Payload)
	if e != nil {
		return Artifact{}, ErrUnavailable
	}
	var a Artifact
	if json.Unmarshal([]byte(plain), &a) != nil || a.Owner != sc.OwnerID || time.Now().After(a.ExpiresAt) {
		return Artifact{}, ErrDenied
	}
	return a, nil
}

// Policy describes installation admission, approved origins and the policy epoch.
func (s *Service) Policy() Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.policy
	p.AllowedOrigins = append([]string(nil), p.AllowedOrigins...)
	return p
}

// SetEnabled applies the trusted configuration kill switch.
func (s *Service) SetEnabled(v bool) error { return s.SetEnabledBy(v, "configuration") }

// SetEnabledBy records the trusted administrator changing installation admission.
func (s *Service) SetEnabledBy(v bool, actor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.broken {
		return ErrUnavailable
	}
	if v && !s.deploymentEnabled {
		return ErrUnavailable
	}
	s.policy.UpdatedBy = actor
	s.policy.Enabled = v
	s.policy.Revision++
	for id := range s.connections {
		s.stopLocked(id)
	}
	return s.saveLocked("installation", "mcp-policy", "policy", s.policy, "policy_changed")
}

// Close closes admission, drains active work and releases owned runtime resources.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	for _, cancel := range s.probes {
		cancel()
	}
	for _, cancel := range s.active {
		cancel()
	}
	s.mu.Unlock()
	s.workers.Wait()
	if runtime, ok := s.protocol.(interface{ Close() error }); ok {
		if err := runtime.Close(); err != nil {
			return err
		}
	}
	return s.lock.Unlock()
}

func project(raw json.RawMessage) (string, bool) {
	// Binary content is retained privately; only bounded text and structured JSON
	// are eligible for model context. Foreign messages/roles remain inert JSON.
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	parts := []string{}
	for _, key := range []string{"structuredContent"} {
		if b := v[key]; len(b) > 0 {
			parts = append(parts, string(b))
		}
	}
	for _, key := range []string{"content", "contents", "messages"} {
		var list []map[string]json.RawMessage
		if json.Unmarshal(v[key], &list) == nil {
			for _, it := range list {
				var text string
				if rawText, ok := it["text"]; ok && json.Unmarshal(rawText, &text) != nil {
					continue
				}
				if text == "" && len(it["content"]) > 0 {
					var nested map[string]json.RawMessage
					if json.Unmarshal(it["content"], &nested) != nil {
						continue
					}
					if json.Unmarshal(nested["text"], &text) != nil {
						continue
					}
				}
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) <= 16<<10 {
		return text, false
	}
	text = text[:16<<10]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

// sweep enforces private-payload retention independently of API access.
func (s *Service) sweep() {
	defer s.workers.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.cleanupExpired()
		select {
		case <-s.root.Done():
			return
		case <-ticker.C:
		}
	}
}

// Retention I/O never holds the admission mutex. A bounded batch and deadline
// keep slow storage from freezing disconnect, confirmation or cancellation.
func (s *Service) cleanupExpired() {
	type expiredRecord struct{ id, owner string }
	expired := make([]expiredRecord, 0, 100)
	now := time.Now()
	s.mu.Lock()
	for id, p := range s.plans {
		if now.After(p.ExpiresAt) {
			delete(s.plans, id)
		}
	}
	for id, a := range s.oauthExpiry {
		if now.After(a.expires) && len(expired) < 100 {
			expired = append(expired, expiredRecord{id, a.owner})
		}
	}
	for id, a := range s.artifacts {
		if now.After(a.expires) && len(expired) < 100 {
			expired = append(expired, expiredRecord{id, a.owner})
		}
	}
	for id, x := range s.executions {
		if !x.FinishedAt.IsZero() && now.Sub(x.FinishedAt) > 30*24*time.Hour && len(expired) < 100 {
			expired = append(expired, expiredRecord{id, x.Owner})
		}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(s.root, 10*time.Second)
	defer cancel()
	for _, record := range expired {
		if err := s.repo.DeleteMCPRecord(ctx, record.owner, record.id); err != nil {
			break
		}
		s.mu.Lock()
		delete(s.oauthExpiry, record.id)
		delete(s.artifacts, record.id)
		delete(s.executions, record.id)
		delete(s.revisions, record.id)
		s.mu.Unlock()
	}
	if err := s.repo.PruneMCPEvents(ctx, now.Add(-30*24*time.Hour)); err != nil && ctx.Err() == nil {
		slog.Warn("MCP audit retention cleanup incomplete")
	}
}

// Audit returns owner-scoped safe append-only metadata.
func (s *Service) Audit(ctx context.Context, sc Scope) ([]domain.MCPRecordEvent, error) {
	if s.admit(ctx, sc) != nil || sc.CallerID != "dashboard" {
		return nil, ErrDenied
	}
	v, e := s.repo.ListMCPEvents(ctx, sc.OwnerID)
	if e != nil {
		return nil, ErrUnavailable
	}
	if v == nil {
		v = []domain.MCPRecordEvent{}
	}
	return v, nil
}
