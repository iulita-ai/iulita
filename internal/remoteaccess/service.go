// Package remoteaccess owns authorization and durable native SSH executions.
package remoteaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"golang.org/x/crypto/ssh"
)

// ErrDenied reports an authorization or consent failure.
var ErrDenied = errors.New("remote access denied")

// Scope identifies the authenticated owner and trusted calling adapter.
type Scope struct {
	OwnerID  string `json:"owner_id"`
	CallerID string `json:"caller_id"`
}

// Server describes a pinned SSH endpoint owned by one principal.
type Server struct {
	ID          string   `json:"id"`
	OwnerID     string   `json:"owner_id"`
	Label       string   `json:"label"`
	Address     string   `json:"address"`
	Port        int      `json:"port"`
	HostKeys    []string `json:"host_keys"`
	TrustSource string   `json:"trust_source"`
	Enabled     bool     `json:"enabled"`
	Revision    int      `json:"revision"`
}

// Identity references encrypted authentication material and safe public metadata.
type Identity struct {
	PublicKey         string    `json:"public_key,omitempty"`
	PublicFingerprint string    `json:"public_fingerprint,omitempty"`
	ID                string    `json:"id"`
	OwnerID           string    `json:"owner_id"`
	Username          string    `json:"username"`
	AuthType          string    `json:"auth_type"`
	CredentialRef     string    `json:"credential_ref"`
	Enabled           bool      `json:"enabled"`
	ExpiresAt         time.Time `json:"expires_at"`
	Revision          int       `json:"revision"`
}

// Hop binds one route endpoint to its authentication identity.
type Hop struct {
	ServerID   string `json:"server_id"`
	IdentityID string `json:"identity_id"`
}

// Route contains an ordered sequence of bastions and the destination.
type Route struct {
	ID       string `json:"id"`
	OwnerID  string `json:"owner_id"`
	Label    string `json:"label"`
	Hops     []Hop  `json:"hops"`
	Enabled  bool   `json:"enabled"`
	Revision int    `json:"revision"`
}

// Grant authorizes one caller and capability against an exact route revision.
type Grant struct {
	FileRoot        string    `json:"file_root,omitempty"`
	RunbookID       string    `json:"runbook_id,omitempty"`
	RunbookRevision int       `json:"runbook_revision,omitempty"`
	MaxUses         int       `json:"max_uses,omitempty"`
	Uses            int       `json:"uses,omitempty"`
	ID              string    `json:"id"`
	OwnerID         string    `json:"owner_id"`
	CallerID        string    `json:"caller_id"`
	RouteID         string    `json:"route_id"`
	RouteRevision   int       `json:"route_revision"`
	Capability      string    `json:"capability"`
	ExpiresAt       time.Time `json:"expires_at"`
	Permanent       bool      `json:"permanent"`
	Enabled         bool      `json:"enabled"`
}

// ActiveAt reports whether an enabled grant has an explicit valid lifetime.
// A missing expiry on a legacy timed grant never authorizes access.
func (g Grant) ActiveAt(now time.Time) bool {
	if !g.Enabled {
		return false
	}
	if g.Permanent {
		return g.ExpiresAt.IsZero()
	}
	return g.ExpiresAt.After(now)
}

// Secret is write-only authentication material supplied outside model input.
type Secret struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

// ExecuteRequest specifies an exact command and optional one-use approval.
type ExecuteRequest struct {
	Operation        string             `json:"operation,omitempty"`
	Terminal         *TerminalOptions   `json:"terminal,omitempty"`
	File             *FileRequest       `json:"file,omitempty"`
	Background       *BackgroundRequest `json:"background,omitempty"`
	RunbookID        string             `json:"runbook_id,omitempty"`
	RunbookRevision  int                `json:"runbook_revision,omitempty"`
	ApprovalID       string             `json:"approval_id,omitempty"`
	Capability       string             `json:"capability,omitempty"`
	ExpectedPlanHash string             `json:"expected_plan_hash,omitempty"`
	RouteID          string             `json:"route_id"`
	Command          string             `json:"command"`
	TimeoutSeconds   int                `json:"timeout_seconds"`
}

// Approval is the immutable human-visible operation preview.
type Approval struct {
	GrantID                string     `json:"grant_id"`
	AuthorizationExpiresAt time.Time  `json:"authorization_expires_at"`
	ApprovalID             string     `json:"approval_id"`
	CredentialVersions     []string   `json:"credential_versions"`
	ExecutionID            string     `json:"execution_id"`
	PlanHash               string     `json:"plan_hash"`
	Command                string     `json:"command"`
	Route                  Route      `json:"route"`
	Servers                []Server   `json:"servers"`
	Identities             []Identity `json:"identities"`
	ExpiresAt              time.Time  `json:"expires_at"`
}

// ConfirmFunc asks the authenticated owner to approve one immutable operation.
type ConfirmFunc func(context.Context, Approval) (bool, error)

// Execution contains metadata only; raw output resides in encrypted artifacts.
type Execution struct {
	Operation   string    `json:"operation,omitempty"`
	ID          string    `json:"id"`
	OwnerID     string    `json:"owner_id"`
	CallerID    string    `json:"caller_id"`
	RouteID     string    `json:"route_id"`
	PlanHash    string    `json:"plan_hash"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	FinishedAt  time.Time `json:"finished_at"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	Truncated   bool      `json:"truncated"`
	StdoutBytes int       `json:"stdout_bytes"`
	StderrBytes int       `json:"stderr_bytes"`
	ArtifactID  string    `json:"artifact_id,omitempty"`
	ApprovedAt  time.Time `json:"approved_at"`
}

// Artifact contains private command details and bounded remote output.
type Artifact struct {
	File       *FileResult       `json:"file,omitempty"`
	Background *BackgroundResult `json:"background,omitempty"`
	Command    string            `json:"command"`
	Stdout     string            `json:"stdout"`
	Stderr     string            `json:"stderr"`
}
type sealedArtifact struct {
	OwnerID    string    `json:"owner_id"`
	Ciphertext string    `json:"ciphertext"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type storedApproval struct {
	OwnerID     string    `json:"owner_id"`
	CallerID    string    `json:"caller_id"`
	PlanHash    string    `json:"plan_hash"`
	ExpiresAt   time.Time `json:"expires_at"`
	ExecutionID string    `json:"execution_id"`
}

// Catalog is the durable remote access inventory and execution metadata.
type Catalog struct {
	ScheduledTaskFloor int64                      `json:"scheduled_task_floor,omitempty"`
	ScheduledRuns      map[string]ScheduledRun    `json:"scheduled_runs,omitempty"`
	Jobs               map[string]RemoteJob       `json:"jobs,omitempty"`
	Runbooks           map[string]Runbook         `json:"runbooks,omitempty"`
	Approvals          map[string]storedApproval  `json:"approvals,omitempty"`
	PolicyRevision     uint64                     `json:"policy_revision"`
	Installations      map[string]KeyInstallation `json:"installations"`
	Enabled            bool                       `json:"enabled"`
	Servers            map[string]Server          `json:"servers"`
	Identities         map[string]Identity        `json:"identities"`
	Routes             map[string]Route           `json:"routes"`
	Grants             map[string]Grant           `json:"grants"`
	Executions         map[string]Execution       `json:"executions"`
	Artifacts          map[string]sealedArtifact  `json:"artifacts,omitempty"`
}

// Transport executes exactly one request without automatic retries.
type Transport interface {
	Run(context.Context, TransportRequest) (TransportResult, error)
}

// Service authorizes, records and dispatches owner-scoped SSH operations.
type Service struct {
	principalChecker func(context.Context, string) bool
	lock             *flock.Flock
	broken           bool
	closed           bool
	mu               sync.Mutex
	path             string
	vault            *credential.Store
	transport        Transport
	data             Catalog
	active           map[string]context.CancelFunc
}

func newID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

// NewService opens a private, exclusively locked persistent catalog.
func NewService(path string, vault *credential.Store, t Transport) (*Service, error) {
	s := &Service{path: path, vault: vault, transport: t, active: map[string]context.CancelFunc{}, data: Catalog{Approvals: map[string]storedApproval{}, Servers: map[string]Server{}, Identities: map[string]Identity{}, Routes: map[string]Route{}, Grants: map[string]Grant{}, Executions: map[string]Execution{}, Artifacts: map[string]sealedArtifact{}, Installations: map[string]KeyInstallation{}}}
	if path == "" {
		return nil, fmt.Errorf("persistent catalog path required")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return nil, e
	}
	for _, p := range []string{path, path + ".lock"} {
		if st, se := os.Lstat(p); se == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0) {
			return nil, fmt.Errorf("unsafe SSH catalog permissions or symlink")
		}
	}
	s.lock = flock.New(path + ".lock")
	locked, le := s.lock.TryLock()
	if le != nil || !locked {
		return nil, fmt.Errorf("SSH catalog already in use")
	}
	ok := false
	defer func() {
		if !ok {
			ignoreCleanupError(s.lock.Unlock())
		}
	}()
	if err := os.Chmod(path+".lock", 0o600); err != nil {
		return nil, err
	}

	if st, se := os.Stat(path); se == nil && st.Size() > 128<<20 {
		return nil, fmt.Errorf("SSH catalog exceeds storage limit")
	}
	b, e := os.ReadFile(path) // #nosec G304 -- private configured catalog path validated above.
	if e == nil {
		if decodeErr := json.Unmarshal(b, &s.data); decodeErr != nil {
			return nil, decodeErr
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if s.data.Servers == nil || s.data.Identities == nil || s.data.Routes == nil || s.data.Grants == nil || s.data.Executions == nil || s.data.Artifacts == nil {
		return nil, fmt.Errorf("invalid SSH catalog")
	}
	if s.data.ScheduledRuns == nil {
		s.data.ScheduledRuns = map[string]ScheduledRun{}
	}
	if s.data.Jobs == nil {
		s.data.Jobs = map[string]RemoteJob{}
	}
	if s.data.Runbooks == nil {
		s.data.Runbooks = map[string]Runbook{}
	}
	if s.data.Installations == nil {
		s.data.Installations = map[string]KeyInstallation{}
	}
	if s.data.Approvals == nil {
		s.data.Approvals = map[string]storedApproval{}
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for id, x := range s.data.Installations {
		switch x.State {
		case "install_pending":
			x.State = "install_unknown"
		case "verification_pending":
			x.State = "installed_unverified"
		case "remove_pending":
			x.State = "remove_unknown"
		}
		s.data.Installations[id] = x
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for id, x := range s.data.Executions {
		switch x.State {
		case "dispatch_started":
			x.State = "unknown_outcome"
			x.ErrorCode = "control_plane_restarted"
			x.FinishedAt = time.Now()
			s.data.Executions[id] = x
		case "awaiting_approval":
			x.State = "cancelled"
			x.FinishedAt = time.Now()
			s.data.Executions[id] = x
		}
	}
	if e := s.save(); e != nil {
		return nil, e
	}
	ok = true
	return s, nil
}
func (s *Service) save() (err error) {
	if s.closed {
		return fmt.Errorf("SSH catalog closed")
	}
	defer func() {
		if err != nil {
			s.broken = true
		}
	}()
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for id, x := range s.data.Executions {
		if !x.FinishedAt.IsZero() && x.FinishedAt.Before(time.Now().Add(-90*24*time.Hour)) {
			delete(s.data.Executions, id)
		}
	}
	for id, a := range s.data.Approvals {
		if (a.ExecutionID == "" && !a.ExpiresAt.After(time.Now())) || (a.ExecutionID != "" && s.data.Executions[a.ExecutionID].ID == "") {
			delete(s.data.Approvals, id)
		}
	}
	for id, a := range s.data.Artifacts {
		if !a.ExpiresAt.After(time.Now()) {
			delete(s.data.Artifacts, id)
		}
	}
	b, e := json.Marshal(s.data)
	if e != nil {
		return e
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(s.path), 0o700); mkdirErr != nil {
		return mkdirErr
	}
	f, e := os.CreateTemp(filepath.Dir(s.path), ".ssh-catalog-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { ignoreCleanupError(os.Remove(name)) }() // Best effort cleanup after an atomic rename.
	if e = f.Chmod(0o600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if renameErr := os.Rename(name, s.path); renameErr != nil {
		return renameErr
	}
	d, e := os.Open(filepath.Dir(s.path))
	if e != nil {
		return e
	}
	defer func() { ignoreCleanupError(d.Close()) }() // Directory descriptor is read-only; Sync errors are returned.
	return d.Sync()
}
func validScope(sc Scope) bool { return sc.OwnerID != "" && sc.CallerID != "" }

// PutServer creates or revises owner-scoped endpoint trust.
func (s *Service) PutServer(sc Scope, x Server) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validScope(sc) {
		return x, ErrDenied
	}
	if x.ID != "" {
		old, ok := s.data.Servers[x.ID]
		if !ok || old.OwnerID != sc.OwnerID {
			return x, ErrDenied
		}
		x.Revision = old.Revision + 1
	} else {
		x.ID = newID()
		x.Revision = 1
	}
	x.OwnerID = sc.OwnerID
	if strings.TrimSpace(x.Address) != x.Address || x.Address == "" || strings.ContainsAny(x.Address, " /\\\x00\r\n") || x.Port < 1 || x.Port > 65535 {
		return x, fmt.Errorf("invalid endpoint")
	}
	if net.ParseIP(x.Address) == nil && strings.Contains(x.Address, ":") {
		return x, fmt.Errorf("invalid hostname")
	}
	if len(x.HostKeys) == 0 || x.TrustSource == "" {
		return x, fmt.Errorf("verified host keys and trust source required")
	}
	for _, k := range x.HostKeys {
		_, _, _, rest, e := ssh.ParseAuthorizedKey([]byte(k))
		if e != nil || strings.TrimSpace(string(rest)) != "" {
			return x, fmt.Errorf("invalid host key")
		}
	}
	x.HostKeys = append([]string(nil), x.HostKeys...)
	s.data.Servers[x.ID] = x
	s.cancelOwner(sc.OwnerID)
	s.data.PolicyRevision++
	err := s.save()
	x.HostKeys = append([]string(nil), x.HostKeys...)
	return x, err
}

// PutIdentity stores authentication material in the encrypted credential vault.
func (s *Service) PutIdentity(ctx context.Context, sc Scope, x Identity, secret *Secret) (Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validScope(sc) || s.vault == nil || !s.vault.EncryptionEnabled() {
		return x, ErrDenied
	}
	if x.ID != "" {
		old, ok := s.data.Identities[x.ID]
		if !ok || old.OwnerID != sc.OwnerID {
			return x, ErrDenied
		}
		x.Revision = old.Revision + 1
		x.CredentialRef = old.CredentialRef
		if secret == nil && x.AuthType != old.AuthType {
			return x, fmt.Errorf("new secret required when changing authentication type")
		}
		x.PublicKey = old.PublicKey
		x.PublicFingerprint = old.PublicFingerprint
	} else {
		x.ID = newID()
		x.Revision = 1
		x.CredentialRef = "ssh.identity." + x.ID
		if secret == nil {
			return x, fmt.Errorf("secret required")
		}
	}
	x.OwnerID = sc.OwnerID
	if x.AuthType == "password" {
		x.PublicKey = ""
		x.PublicFingerprint = ""
	}
	if x.Username == "" || strings.ContainsAny(x.Username, "\x00\r\n") {
		return x, fmt.Errorf("invalid username")
	}
	if x.AuthType != "password" && x.AuthType != "private_key" {
		return x, fmt.Errorf("unsupported auth type")
	}
	if secret != nil {
		if x.AuthType == "password" {
			if secret.Password == "" || secret.PrivateKey != "" || secret.Passphrase != "" {
				return x, fmt.Errorf("invalid password identity")
			}
		} else {
			var e error
			var signer ssh.Signer
			if secret.Passphrase != "" {
				signer, e = ssh.ParsePrivateKeyWithPassphrase([]byte(secret.PrivateKey), []byte(secret.Passphrase))
			} else {
				signer, e = ssh.ParsePrivateKey([]byte(secret.PrivateKey))
			}
			if e != nil || secret.Password != "" {
				return x, fmt.Errorf("invalid private key")
			}
			x.PublicKey = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
			x.PublicFingerprint = ssh.FingerprintSHA256(signer.PublicKey())
		}
		b, e := json.Marshal(secret) // #nosec G117 -- write-only material is serialized directly into the encrypted vault, never a response.
		if e != nil {
			return x, e
		}
		_, e = s.vault.Set(ctx, credential.SetRequest{Name: x.CredentialRef, Type: domain.CredentialType("ssh_identity"), Scope: domain.CredentialScopeUser, OwnerID: sc.OwnerID, Value: string(b), UpdatedBy: sc.OwnerID})
		if e != nil {
			return x, fmt.Errorf("cannot store SSH identity")
		}
	}
	s.data.Identities[x.ID] = x
	s.cancelOwner(sc.OwnerID)
	s.data.PolicyRevision++
	return x, s.save()
}

// PutRoute validates and records an ordered owner-scoped route.
func (s *Service) PutRoute(sc Scope, x Route) (Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validScope(sc) {
		return x, ErrDenied
	}
	if len(x.Hops) < 1 || len(x.Hops) > 4 {
		return x, fmt.Errorf("route requires 1 to 4 endpoints")
	}
	seen := map[string]bool{}
	for _, h := range x.Hops {
		v, ok := s.data.Servers[h.ServerID]
		i, iok := s.data.Identities[h.IdentityID]
		if !ok || !iok || v.OwnerID != sc.OwnerID || i.OwnerID != sc.OwnerID || seen[h.ServerID] {
			return x, ErrDenied
		}
		seen[h.ServerID] = true
	}
	if x.ID != "" {
		old, ok := s.data.Routes[x.ID]
		if !ok || old.OwnerID != sc.OwnerID {
			return x, ErrDenied
		}
		x.Revision = old.Revision + 1
	} else {
		x.ID = newID()
		x.Revision = 1
	}
	x.OwnerID = sc.OwnerID
	x.Hops = append([]Hop(nil), x.Hops...)
	s.data.Routes[x.ID] = x
	s.cancelOwner(sc.OwnerID)
	s.data.PolicyRevision++
	err := s.save()
	x.Hops = append([]Hop(nil), x.Hops...)
	return x, err
}

// PutGrant authorizes one caller against an exact route revision.
func (s *Service) PutGrant(sc Scope, x Grant) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Routes[x.RouteID]
	now := time.Now()
	validLifetime := x.Permanent && x.ExpiresAt.IsZero() || !x.Permanent && x.ExpiresAt.After(now) && !x.ExpiresAt.After(now.Add(24*time.Hour))
	if !validScope(sc) || !ok || r.OwnerID != sc.OwnerID || x.CallerID == "" || !validCapability(x.Capability) || !validLifetime {
		return x, ErrDenied
	}
	if x.ID != "" {
		old, ok := s.data.Grants[x.ID]
		if !ok || old.OwnerID != sc.OwnerID {
			return x, ErrDenied
		}
	} else {
		x.ID = newID()
	}
	if x.RunbookID != "" {
		rb, ok := s.data.Runbooks[x.RunbookID]
		if !ok || rb.OwnerID != sc.OwnerID || !rb.Enabled || rb.RouteID != x.RouteID || x.Capability != "runbook" || x.MaxUses < 1 || x.MaxUses > 1000 {
			return x, ErrDenied
		}
		x.RunbookRevision = rb.Revision
		x.Uses = 0
	} else if x.Capability == "runbook" {
		return x, ErrDenied
	}
	if (strings.HasPrefix(x.Capability, "file_") || strings.HasPrefix(x.Capability, "background_")) && !validFileRoot(x.FileRoot) {
		return x, ErrDenied
	}
	x.OwnerID = sc.OwnerID
	x.RouteRevision = r.Revision
	s.data.Grants[x.ID] = x
	s.cancelOwner(sc.OwnerID)
	s.data.PolicyRevision++
	return x, s.save()
}
func (s *Service) cancelOwner(owner string) {
	for id, c := range s.active {
		if s.data.Executions[id].OwnerID == owner {
			c()
		}
	}
}

// SetEnabled changes global admission and cancels active work on disable.
func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled && (s.broken || s.closed || s.vault == nil || !s.vault.EncryptionEnabled() || s.transport == nil) {
		return ErrDenied
	}
	s.data.Enabled = enabled
	s.data.PolicyRevision++
	if !enabled {
		for _, c := range s.active {
			c()
		}
	}
	return s.save()
}

// Cancel disconnects an owner operation without promising remote termination.
func (s *Service) Cancel(sc Scope, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.data.Executions[id]
	if !validScope(sc) || !ok || x.OwnerID != sc.OwnerID {
		return ErrDenied
	}
	if c := s.active[id]; c != nil {
		c()
	}
	return nil
}

// Snapshot returns a detached owner-scoped metadata inventory.
func (s *Service) Snapshot(sc Scope) Catalog {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Catalog{Jobs: map[string]RemoteJob{}, Runbooks: map[string]Runbook{}, Enabled: s.data.Enabled, Servers: map[string]Server{}, Identities: map[string]Identity{}, Routes: map[string]Route{}, Grants: map[string]Grant{}, Executions: map[string]Execution{}, Installations: map[string]KeyInstallation{}}
	if !validScope(sc) {
		return out
	}
	for k, x := range s.data.Jobs {
		if x.OwnerID == sc.OwnerID {
			out.Jobs[k] = x
		}
	}
	for k, x := range s.data.Runbooks {
		if x.OwnerID == sc.OwnerID {
			out.Runbooks[k] = cloneRunbook(x)
		}
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for k, x := range s.data.Servers {
		if x.OwnerID == sc.OwnerID {
			out.Servers[k] = x
		}
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for k, x := range s.data.Identities {
		if x.OwnerID == sc.OwnerID {
			out.Identities[k] = x
		}
	}
	for k, x := range s.data.Routes {
		if x.OwnerID == sc.OwnerID {
			out.Routes[k] = x
		}
	}
	//nolint:gocritic // Immutable snapshot values intentionally copy grant metadata.
	for k, x := range s.data.Grants {
		if x.OwnerID == sc.OwnerID {
			out.Grants[k] = x
		}
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for k, x := range s.data.Executions {
		if x.OwnerID == sc.OwnerID {
			out.Executions[k] = x
		}
	}
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for k, x := range s.data.Installations {
		if x.OwnerID == sc.OwnerID {
			out.Installations[k] = x
		}
	}
	//nolint:gocritic // Detached map values intentionally retain immutable snapshots.
	for id, v := range out.Servers {
		v.HostKeys = append([]string(nil), v.HostKeys...)
		out.Servers[id] = v
	}
	for id, v := range out.Routes {
		v.Hops = append([]Hop(nil), v.Hops...)
		out.Routes[id] = v
	}
	return out
}

// Artifact decrypts one unexpired private artifact for its owner.
func (s *Service) Artifact(sc Scope, id string) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data.Artifacts[id]
	if !validScope(sc) || !ok || a.OwnerID != sc.OwnerID || !a.ExpiresAt.After(time.Now()) {
		return Artifact{}, ErrDenied
	}
	v, e := s.vault.OpenPrivate(a.Ciphertext)
	if e != nil {
		return Artifact{}, e
	}
	var out Artifact
	e = json.Unmarshal([]byte(v), &out)
	return out, e
}

// SetPrincipalChecker configures the trusted account-state check used at admission
// and periodically for active operations. Production adapters must configure it.
func (s *Service) SetPrincipalChecker(check func(context.Context, string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principalChecker = check
}
func (s *Service) plan(sc Scope, req ExecuteRequest) (Approval, error) {
	if s.principalChecker != nil {
		checkctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		allowed := s.principalChecker(checkctx, sc.OwnerID) && checkctx.Err() == nil
		cancel()
		if !allowed {
			return Approval{}, ErrDenied
		}
	}

	if req.Capability == "" {
		req.Capability = "exec"
	}
	r, ok := s.data.Routes[req.RouteID]
	if !validScope(sc) || (!s.data.Enabled && !recoveryOperation(req)) || s.broken || !ok || (!r.Enabled && !recoveryOperation(req)) || r.OwnerID != sc.OwnerID || s.transport == nil || s.vault == nil || !s.vault.EncryptionEnabled() {
		return Approval{}, ErrDenied
	}
	granted := false
	var matched Grant
	//nolint:gocritic // Select an immutable grant snapshot for the consent hash.
	for _, g := range s.data.Grants {
		if g.ActiveAt(time.Now()) && g.OwnerID == sc.OwnerID && g.CallerID == sc.CallerID && g.RouteID == r.ID && g.RouteRevision == r.Revision && g.Capability == req.Capability && grantMatches(g, req, s.data.Runbooks) {
			granted = true
			if matched.ID == "" || g.ID < matched.ID {
				matched = g
			}
		}
	}
	if !granted {
		return Approval{}, ErrDenied
	}
	if req.Background != nil && req.Operation != "background_install" {
		job, exists := s.data.Jobs[req.Background.JobID]
		if req.Operation == "background_start" {
			if exists {
				return Approval{}, ErrDenied
			}
		} else if !exists || job.OwnerID != sc.OwnerID || job.RouteID != req.RouteID || job.Root != req.Background.Root {
			return Approval{}, ErrDenied
		}
	}
	a := Approval{GrantID: matched.ID, Command: req.Command, Route: r, AuthorizationExpiresAt: matched.ExpiresAt}
	for _, h := range r.Hops {
		v, ok := s.data.Servers[h.ServerID]
		i, iok := s.data.Identities[h.IdentityID]
		if !ok || !iok || (!v.Enabled && !recoveryOperation(req)) || !i.Enabled || v.OwnerID != sc.OwnerID || i.OwnerID != sc.OwnerID || (!i.ExpiresAt.IsZero() && !i.ExpiresAt.After(time.Now())) {
			return a, ErrDenied
		}
		a.Servers = append(a.Servers, v)
		a.Identities = append(a.Identities, i)
		if !i.ExpiresAt.IsZero() && (a.AuthorizationExpiresAt.IsZero() || i.ExpiresAt.Before(a.AuthorizationExpiresAt)) {
			a.AuthorizationExpiresAt = i.ExpiresAt
		}
		vhash, ve := s.vault.PrivateVersion(context.Background(), sc.OwnerID, i.CredentialRef, "ssh_identity")
		if ve != nil {
			return a, ErrDenied
		}
		a.CredentialVersions = append(a.CredentialVersions, vhash)
	}
	b, marshalErr := json.Marshal(struct {
		Scope          Scope
		Request        ExecuteRequest
		Plan           Approval
		Grant          Grant
		PolicyRevision uint64
	}{sc, canonicalRequest(req), a, matched, s.data.PolicyRevision})
	if marshalErr != nil {
		return Approval{}, marshalErr
	}
	h := sha256.Sum256(b)
	a.PlanHash = hex.EncodeToString(h[:])
	return a, nil
}

// Execute authorizes and confirms an arbitrary SSH command.
func (s *Service) Execute(ctx context.Context, sc Scope, req ExecuteRequest, confirm ConfirmFunc) (Execution, error) {
	if (req.Capability != "" && req.Capability != "exec") || req.Operation != "" || req.File != nil || req.Background != nil || req.Terminal != nil || req.RunbookID != "" || req.RunbookRevision != 0 {
		return Execution{}, ErrDenied
	}
	return s.execute(ctx, sc, req, confirm)
}
func (s *Service) execute(ctx context.Context, sc Scope, req ExecuteRequest, confirm ConfirmFunc) (Execution, error) {
	return s.executeWithTransport(ctx, sc, req, confirm, nil)
}
func (s *Service) executeWithTransport(ctx context.Context, sc Scope, req ExecuteRequest, confirm ConfirmFunc, override Transport) (Execution, error) {
	// Freeze caller-owned pointers before any approval wait.
	frozen, freezeErr := json.Marshal(req)
	if freezeErr != nil {
		return Execution{}, ErrDenied
	}
	if json.Unmarshal(frozen, &req) != nil {
		return Execution{}, ErrDenied
	}
	maxTimeout := 300
	if override != nil && req.Operation == "terminal" {
		maxTimeout = 3600
	}
	if req.Command == "" || len(req.Command) > 64*1024 || strings.ContainsRune(req.Command, 0) || req.TimeoutSeconds < 0 || req.TimeoutSeconds > maxTimeout {
		return Execution{}, fmt.Errorf("invalid command or timeout")
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 60
	}
	if confirm == nil {
		return Execution{}, ErrDenied
	}
	s.mu.Lock()
	if req.ApprovalID != "" {
		pa, ok := s.data.Approvals[req.ApprovalID]
		if !ok || pa.OwnerID != sc.OwnerID || pa.CallerID != sc.CallerID || pa.PlanHash != req.ExpectedPlanHash {
			s.mu.Unlock()
			return Execution{}, ErrDenied
		}
		if pa.ExecutionID != "" {
			prior := s.data.Executions[pa.ExecutionID]
			s.mu.Unlock()
			return prior, nil
		}
		if !pa.ExpiresAt.After(time.Now()) {
			s.mu.Unlock()
			return Execution{}, ErrDenied
		}
	}
	a, e := s.plan(sc, req)
	if e != nil {
		s.mu.Unlock()
		return Execution{}, e
	}
	if req.ExpectedPlanHash != "" && req.ExpectedPlanHash != a.PlanHash {
		s.mu.Unlock()
		return Execution{}, ErrDenied
	}
	count := 0
	//nolint:gocritic // Map values are copied deliberately for immutable snapshots and reconciliation.
	for _, prior := range s.data.Executions {
		if prior.OwnerID == sc.OwnerID {
			count++
		}
	}
	for count >= 1000 {
		oldest := ""
		//nolint:gocritic // Select a finished immutable history snapshot; active intents are never evicted.
		for id, prior := range s.data.Executions {
			if prior.OwnerID == sc.OwnerID && !prior.FinishedAt.IsZero() {
				if oldest == "" || prior.FinishedAt.Before(s.data.Executions[oldest].FinishedAt) {
					oldest = id
				}
			}
		}
		if oldest == "" {
			s.mu.Unlock()
			return Execution{}, fmt.Errorf("execution retention quota exceeded")
		}
		delete(s.data.Executions, oldest)
		count--
	}
	n := 0
	//nolint:gocritic // Count immutable execution snapshots while holding the catalog lock.
	for _, x := range s.data.Executions {
		if x.OwnerID == sc.OwnerID && (x.State == "awaiting_approval" || x.State == "dispatch_started") {
			n++
		}
	}
	if (n >= 2 && !recoveryOperation(req)) || n >= 4 {
		s.mu.Unlock()
		return Execution{}, fmt.Errorf("concurrency limit")
	}
	x := Execution{Operation: req.Operation, ID: newID(), OwnerID: sc.OwnerID, CallerID: sc.CallerID, RouteID: req.RouteID, PlanHash: a.PlanHash, State: "awaiting_approval", CreatedAt: time.Now()}
	intentBytes, marshalErr := json.Marshal(Artifact{Command: req.Command})
	if marshalErr != nil {
		s.mu.Unlock()
		return Execution{}, marshalErr
	}
	intentCipher, ie := s.vault.SealPrivate(string(intentBytes))
	if ie != nil {
		s.mu.Unlock()
		return Execution{}, fmt.Errorf("cannot persist private command intent")
	}
	x.ArtifactID = newID()
	s.data.Artifacts[x.ArtifactID] = sealedArtifact{OwnerID: sc.OwnerID, Ciphertext: intentCipher, ExpiresAt: time.Now().Add(24 * time.Hour)}
	a.ExecutionID = x.ID
	a.ExpiresAt = time.Now().Add(5 * time.Minute)
	deadline := time.Now().Add(time.Duration(req.TimeoutSeconds+300) * time.Second)
	if !a.AuthorizationExpiresAt.IsZero() && a.AuthorizationExpiresAt.Before(deadline) {
		deadline = a.AuthorizationExpiresAt
	}
	runctx, cancel := context.WithDeadline(ctx, deadline)
	checker := s.principalChecker
	if checker != nil {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-runctx.Done():
					return
				case <-ticker.C:
					checkctx, finish := context.WithTimeout(runctx, 4*time.Second)
					allowed := checker(checkctx, sc.OwnerID) && checkctx.Err() == nil
					finish()
					if !allowed {
						cancel()
						return
					}
				}
			}
		}()
	}
	s.active[x.ID] = cancel
	s.data.Executions[x.ID] = x
	if req.ApprovalID != "" {
		pa := s.data.Approvals[req.ApprovalID]
		pa.ExecutionID = x.ID
		s.data.Approvals[req.ApprovalID] = pa
	}
	e = s.save()
	s.mu.Unlock()
	if e != nil {
		cancel()
		return Execution{}, e
	}
	defer func() { cancel(); s.mu.Lock(); delete(s.active, x.ID); s.mu.Unlock() }()
	safeApproval := cloneApproval(a)
	approved, ce := confirm(runctx, safeApproval)
	s.mu.Lock()
	latest, pe := s.plan(sc, req)
	if ce != nil || !approved || pe != nil || latest.PlanHash != a.PlanHash || !a.ExpiresAt.After(time.Now()) || runctx.Err() != nil {
		x.State = "cancelled"
		x.FinishedAt = time.Now()
		s.data.Executions[x.ID] = x
		se := s.save()
		s.mu.Unlock()
		if se != nil {
			return x, se
		}
		return x, ErrDenied
	}
	treq := TransportRequest{Operation: req.Operation, File: req.File, Background: req.Background, Command: req.Command, Timeout: time.Duration(req.TimeoutSeconds) * time.Second}
	for j := range a.Servers {
		v := &a.Servers[j]
		i := a.Identities[j]
		value, re := s.vault.ResolvePrivateAtVersion(runctx, sc.OwnerID, i.CredentialRef, "ssh_identity", a.CredentialVersions[j])
		if re != nil {
			x.State = "failed"
			x.ErrorCode = "credential_denied"
			s.data.Executions[x.ID] = x
			se := s.save()
			s.mu.Unlock()
			if se != nil {
				return x, se
			}
			return x, ErrDenied
		}
		var secret Secret
		if json.Unmarshal([]byte(value), &secret) != nil {
			x.State = "failed"
			x.ErrorCode = "credential_invalid"
			s.data.Executions[x.ID] = x
			se := s.save()
			s.mu.Unlock()
			if se != nil {
				return x, se
			}
			return x, ErrDenied
		}
		treq.Endpoints = append(treq.Endpoints, TransportEndpoint{Address: v.Address, Port: v.Port, Username: i.Username, HostKeys: v.HostKeys, Password: secret.Password, PrivateKey: secret.PrivateKey, Passphrase: secret.Passphrase})
	}
	if req.RunbookID != "" {
		consumed := false
		//nolint:gocritic // Copy and persist only the selected authorization grant.
		for id, g := range s.data.Grants {
			if g.ID == a.GrantID && g.ActiveAt(time.Now()) && g.OwnerID == sc.OwnerID && g.CallerID == sc.CallerID && g.RouteID == req.RouteID && g.RouteRevision == a.Route.Revision && g.Capability == req.Capability && grantMatches(g, req, s.data.Runbooks) {
				g.Uses++
				s.data.Grants[id] = g
				consumed = true
				break
			}
		}
		if !consumed {
			s.mu.Unlock()
			return x, ErrDenied
		}
	}
	if req.Operation == "background_start" {
		h := sha256.Sum256([]byte(req.Background.Command))
		s.data.Jobs[req.Background.JobID] = RemoteJob{ID: req.Background.JobID, OwnerID: sc.OwnerID, RouteID: req.RouteID, Root: req.Background.Root, CommandHash: hex.EncodeToString(h[:]), ExecutionID: x.ID, State: "unknown_outcome"}
	}
	x.State = "dispatch_started"
	x.ApprovedAt = time.Now()
	s.data.Executions[x.ID] = x
	if e = s.save(); e != nil {
		s.mu.Unlock()
		return x, e
	}
	s.mu.Unlock()
	execctx, stop := context.WithTimeout(runctx, treq.Timeout)
	defer stop()
	transport := s.transport
	if override != nil {
		transport = override
	}
	result, runerr := transport.Run(execctx, treq)
	s.mu.Lock()
	defer s.mu.Unlock()
	if result.Background != nil && req.Background != nil {
		j, ok := s.data.Jobs[req.Background.JobID]
		if ok && j.OwnerID == sc.OwnerID {
			j.State = result.Background.State
			s.data.Jobs[j.ID] = j
		}
	}
	x.FinishedAt = time.Now()
	x.ExitCode = result.ExitCode
	x.ErrorCode = result.ErrorCode
	x.Truncated = result.Truncated
	x.StdoutBytes = len(result.Stdout)
	x.StderrBytes = len(result.Stderr)
	switch {
	case result.ExitCode != nil:
		x.State = "completed"
		if *result.ExitCode != 0 {
			x.State = "failed"
		}
	case result.Dispatched:
		x.State = "unknown_outcome"
	default:
		x.State = "failed"
	}
	if runerr != nil && x.ErrorCode == "" {
		x.ErrorCode = "transport_error"
	}
	b, marshalErr := json.Marshal(Artifact{File: result.File, Background: result.Background, Command: req.Command, Stdout: string(result.Stdout), Stderr: string(result.Stderr)})
	var sealed string
	se := marshalErr
	if se == nil {
		sealed, se = s.vault.SealPrivate(string(b))
	}
	if se == nil {
		owned := []string{}
		for id, a := range s.data.Artifacts {
			if a.OwnerID == sc.OwnerID {
				owned = append(owned, id)
			}
		}
		if len(owned) >= 32 {
			old := owned[0]
			for _, id := range owned {
				if s.data.Artifacts[id].ExpiresAt.Before(s.data.Artifacts[old].ExpiresAt) {
					old = id
				}
			}
			delete(s.data.Artifacts, old)
		}
		if x.ArtifactID == "" {
			x.ArtifactID = newID()
		}
		// Keep encrypted file artifacts within the catalog reopen bound.
		for {
			total := len(sealed)
			oldest := ""
			for id, a := range s.data.Artifacts {
				if id == x.ArtifactID {
					continue
				}
				total += len(a.Ciphertext)
				if oldest == "" || a.ExpiresAt.Before(s.data.Artifacts[oldest].ExpiresAt) {
					oldest = id
				}
			}
			if total <= 64<<20 || oldest == "" {
				break
			}
			delete(s.data.Artifacts, oldest)
		}
		s.data.Artifacts[x.ArtifactID] = sealedArtifact{OwnerID: sc.OwnerID, Ciphertext: sealed, ExpiresAt: time.Now().Add(24 * time.Hour)}
	} else {
		x.ErrorCode = "artifact_storage_failed"
	}
	s.data.Executions[x.ID] = x
	if e = s.save(); e != nil {
		return x, fmt.Errorf("execution finished; durable reconciliation required")
	}
	return x, nil
}

// Preview freezes the exact owner/caller/route/identity revisions and command.
func (s *Service) Preview(sc Scope, req ExecuteRequest) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 60
	}
	a, e := s.plan(sc, req)
	if e != nil {
		return a, e
	}
	if len(s.data.Approvals) > 10000 {
		return Approval{}, fmt.Errorf("approval quota exceeded")
	}
	a.ApprovalID = newID()
	a.ExpiresAt = time.Now().Add(5 * time.Minute)
	s.data.Approvals[a.ApprovalID] = storedApproval{OwnerID: sc.OwnerID, CallerID: sc.CallerID, PlanHash: a.PlanHash, ExpiresAt: a.ExpiresAt}
	if e := s.save(); e != nil {
		return Approval{}, e
	}
	return cloneApproval(a), nil
}

// Close cancels work and releases the persistent catalog lock.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Enabled = false
	for _, c := range s.active {
		c()
	}
	s.broken = true
	s.closed = true
	return s.lock.Unlock()
}

// CredentialChanged invalidates approvals and disconnects work using a changed
// or deleted vault identity. It must be wired to the vault lifecycle publisher.
func (s *Service) CredentialChanged(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	owners := map[string]bool{}
	//nolint:gocritic // Collect owners from immutable identity metadata.
	for _, i := range s.data.Identities {
		if i.CredentialRef == name {
			owners[i.OwnerID] = true
		}
	}
	if len(owners) == 0 {
		return nil
	}
	for owner := range owners {
		s.cancelOwner(owner)
	}
	s.data.PolicyRevision++
	return s.save()
}

func cloneApproval(a Approval) Approval {
	a.Route.Hops = append([]Hop(nil), a.Route.Hops...)
	a.Identities = append([]Identity(nil), a.Identities...)
	a.CredentialVersions = append([]string(nil), a.CredentialVersions...)
	a.Servers = append([]Server(nil), a.Servers...)
	for i := range a.Servers {
		a.Servers[i].HostKeys = append([]string(nil), a.Servers[i].HostKeys...)
	}
	return a
}

// ignoreCleanupError is reserved for read-only descriptor cleanup and removal of
// temporary files after primary persistence errors have already been handled.
func ignoreCleanupError(_ error) {}
