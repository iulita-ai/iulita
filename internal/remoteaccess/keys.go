package remoteaccess

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/ssh"
)

// KeyInstallation remains in inventory even after local identities are disabled.
// Verified means a separate SSH login succeeded, not merely a file write.
type KeyInstallation struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"owner_id"`
	ServerID    string    `json:"server_id"`
	Username    string    `json:"username"`
	IdentityID  string    `json:"identity_id"`
	PublicKey   string    `json:"public_key"`
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	ExecutionID string    `json:"execution_id"`
	VerifiedAt  time.Time `json:"verified_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GenerateIdentity writes generated private material directly to the encrypted
// credential store. Only public metadata is returned to the caller.
func (s *Service) GenerateIdentity(ctx context.Context, sc Scope, identity Identity) (Identity, error) {
	if identity.ID != "" {
		return Identity{}, errors.New("key generation requires a new identity")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, errors.New("key generation failed")
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return Identity{}, errors.New("key encoding failed")
	}
	identity.AuthType = "private_key"
	return s.PutIdentity(ctx, sc, identity, &Secret{PrivateKey: string(pem.EncodeToMemory(block))})
}

func managedKeyScript(key, id string, remove bool) (string, error) {
	parsed, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil || len(options) != 0 || len(rest) != 0 {
		return "", errors.New("invalid installation public key")
	}
	if len(id) != 32 {
		return "", errors.New("invalid managed key marker")
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", errors.New("invalid managed key marker")
		}
	}
	canonical := string(ssh.MarshalAuthorizedKey(parsed))
	canonical = canonical[:len(canonical)-1] + " iulita:" + id + "\n"
	mode := "False"
	if remove {
		mode = "True"
	}
	// A dedicated directory descriptor and O_NOFOLLOW prevent symlink traversal.
	// flock serializes cooperating changes; metadata is rechecked before rename.
	script := `import os, stat, fcntl, base64, secrets, pwd
line = base64.b64decode('` + base64.StdEncoding.EncodeToString([]byte(canonical)) + `')
remove = ` + mode + `
uid = os.getuid()
home = pwd.getpwuid(uid).pw_dir
assert os.path.isabs(home) and os.path.realpath(home) == home, 'unsafe home'
h = os.stat(home, follow_symlinks=False)
assert h.st_uid == uid and not h.st_mode & 0o022, 'unsafe home permissions'
hfd = os.open(home, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
try:
    try: os.mkdir('.ssh', 0o700, dir_fd=hfd)
    except FileExistsError: pass
    dfd = os.open('.ssh', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=hfd)
finally: os.close(hfd)
try:
    d = os.fstat(dfd)
    assert d.st_uid == uid and not d.st_mode & 0o077, 'unsafe .ssh permissions'
    lfd = os.open('.iulita-authorized-keys.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600, dir_fd=dfd)
    try:
        l = os.fstat(lfd)
        assert stat.S_ISREG(l.st_mode) and l.st_uid == uid and l.st_nlink == 1 and not l.st_mode & 0o077, 'unsafe lock'
        fcntl.flock(lfd, fcntl.LOCK_EX)
        original = None
        try:
            fd = os.open('authorized_keys', os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dfd)
            try:
                original = os.fstat(fd)
                assert stat.S_ISREG(original.st_mode) and original.st_uid == uid and original.st_nlink == 1 and not original.st_mode & 0o077, 'unsafe authorized_keys'
                assert original.st_size <= 1048576, 'authorized_keys too large'
                with os.fdopen(fd, 'rb', closefd=False) as source: data = source.read(1048577)
                assert len(data) <= 1048576, 'authorized_keys too large'
            finally: os.close(fd)
        except FileNotFoundError: data = b''
        lines = data.splitlines(keepends=True)
        if remove:
            assert sum(item == line for item in lines) == 1, 'managed entry missing or ambiguous'
            updated = b''.join(item for item in lines if item != line)
        else:
            marker = line.split()[-1]
            assert not any(marker in item.split() and item != line for item in lines), 'managed marker conflict'
            updated = data if line in lines else data + (b'\n' if data and not data.endswith(b'\n') else b'') + line
        name = '.iulita-keys-' + secrets.token_hex(16)
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=dfd)
        try:
            with os.fdopen(fd, 'wb', closefd=False) as target: target.write(updated); target.flush(); os.fsync(fd)
        finally: os.close(fd)
        try:
            try: current = os.stat('authorized_keys', dir_fd=dfd, follow_symlinks=False)
            except FileNotFoundError: current = None
            assert (original is None and current is None) or (original is not None and current is not None and (original.st_ino, original.st_dev, original.st_mtime_ns, original.st_ctime_ns, original.st_size) == (current.st_ino, current.st_dev, current.st_mtime_ns, current.st_ctime_ns, current.st_size)), 'authorized_keys changed concurrently'
            os.rename(name, 'authorized_keys', src_dir_fd=dfd, dst_dir_fd=dfd)
            os.fsync(dfd)
            fd = os.open('authorized_keys', os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dfd)
            with os.fdopen(fd, 'rb') as target: assert target.read(1048577) == updated, 'readback failed'
        finally:
            try: os.unlink(name, dir_fd=dfd)
            except FileNotFoundError: pass
    finally: os.close(lfd)
finally: os.close(dfd)
`
	return "python3 - <<'IULITA_MANAGED_KEY'\n" + script + "IULITA_MANAGED_KEY\n", nil
}

func (s *Service) keyRoute(sc Scope, routeID string) (Server, Identity, error) {
	r, ok := s.data.Routes[routeID]
	if !validScope(sc) || !ok || r.OwnerID != sc.OwnerID || !r.Enabled || len(r.Hops) == 0 {
		return Server{}, Identity{}, ErrDenied
	}
	h := r.Hops[len(r.Hops)-1]
	v := s.data.Servers[h.ServerID]
	i := s.data.Identities[h.IdentityID]
	if v.OwnerID != sc.OwnerID || i.OwnerID != sc.OwnerID || !v.Enabled || !i.Enabled {
		return Server{}, Identity{}, ErrDenied
	}
	return v, i, nil
}
func (s *Service) saveInstallation(x KeyInstallation) error {
	if s.data.Installations == nil {
		s.data.Installations = map[string]KeyInstallation{}
	}
	x.UpdatedAt = time.Now()
	s.data.Installations[x.ID] = x
	return s.save()
}

// InstallPublicKey adds an exact managed entry through an approved execution.
func (s *Service) InstallPublicKey(ctx context.Context, sc Scope, routeID, identityID string, confirm ConfirmFunc) (KeyInstallation, error) {
	s.mu.Lock()
	server, account, err := s.keyRoute(sc, routeID)
	identity, ok := s.data.Identities[identityID]
	if err != nil || !ok || identity.OwnerID != sc.OwnerID || identity.Username != account.Username || identity.PublicKey == "" || !identity.Enabled {
		s.mu.Unlock()
		return KeyInstallation{}, ErrDenied
	}
	x := KeyInstallation{ID: newID(), OwnerID: sc.OwnerID, ServerID: server.ID, Username: account.Username, IdentityID: identity.ID, PublicKey: identity.PublicKey, Fingerprint: identity.PublicFingerprint, State: "install_pending"}
	script, err := managedKeyScript(x.PublicKey, x.ID, false)
	req := ExecuteRequest{RouteID: routeID, Command: script, Capability: "key_install", TimeoutSeconds: 60}
	if err == nil {
		var approval Approval
		approval, err = s.plan(sc, req)
		req.ExpectedPlanHash = approval.PlanHash
	}
	if err == nil {
		err = s.saveInstallation(x)
	}
	s.mu.Unlock()
	if err != nil {
		return x, err
	}
	execution, runErr := s.execute(ctx, sc, req, confirm)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.ExecutionID = execution.ID
	x.State = "install_failed"
	switch execution.State {
	case "completed":
		x.State = "installed_unverified"
	case "unknown_outcome":
		x.State = "install_unknown"
	}
	if err := s.saveInstallation(x); err != nil {
		return x, err
	}
	return x, runErr
}

// VerifyInstallation requires a separately authorized route using the NEW key.
func (s *Service) VerifyInstallation(ctx context.Context, sc Scope, id, routeID string, confirm ConfirmFunc) (KeyInstallation, error) {
	s.mu.Lock()
	x, ok := s.data.Installations[id]
	server, identity, err := s.keyRoute(sc, routeID)
	if !ok || x.OwnerID != sc.OwnerID || err != nil || x.ServerID != server.ID || x.Username != identity.Username || x.IdentityID != identity.ID || x.Fingerprint != identity.PublicFingerprint || (x.State != "installed_unverified" && x.State != "install_unknown") {
		s.mu.Unlock()
		return KeyInstallation{}, ErrDenied
	}
	req := ExecuteRequest{RouteID: routeID, Command: "true", Capability: "key_install", TimeoutSeconds: 60}
	approval, err := s.plan(sc, req)
	if err != nil {
		s.mu.Unlock()
		return x, err
	}
	req.ExpectedPlanHash = approval.PlanHash
	x.State = "verification_pending"
	err = s.saveInstallation(x)
	s.mu.Unlock()
	if err != nil {
		return x, err
	}
	execution, runErr := s.execute(ctx, sc, req, confirm)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.ExecutionID = execution.ID
	x.State = "installed_unverified"
	if execution.State == "completed" {
		x.State = "verified"
		x.VerifiedAt = time.Now()
	}
	if err := s.saveInstallation(x); err != nil {
		return x, err
	}
	return x, runErr
}

// RemovePublicKey never removes an unmanaged line or the last verified key.
func (s *Service) RemovePublicKey(ctx context.Context, sc Scope, id, alternateID, routeID string, confirm ConfirmFunc) (KeyInstallation, error) {
	s.mu.Lock()
	x, ok := s.data.Installations[id]
	alternate, aok := s.data.Installations[alternateID]
	server, identity, err := s.keyRoute(sc, routeID)
	if !ok || !aok || x.ID == alternate.ID || x.OwnerID != sc.OwnerID || alternate.OwnerID != sc.OwnerID || err != nil || x.ServerID != server.ID || alternate.ServerID != server.ID || x.Username != identity.Username || alternate.Username != identity.Username || alternate.IdentityID != identity.ID || alternate.Fingerprint != identity.PublicFingerprint || alternate.State != "verified" || time.Since(alternate.VerifiedAt) > 5*time.Minute || (x.State != "verified" && x.State != "installed_unverified" && x.State != "install_unknown" && x.State != "remove_failed" && x.State != "remove_unknown") {
		s.mu.Unlock()
		return KeyInstallation{}, ErrDenied
	}
	script, err := managedKeyScript(x.PublicKey, x.ID, true)
	if err != nil {
		s.mu.Unlock()
		return x, err
	}
	req := ExecuteRequest{RouteID: routeID, Command: script, Capability: "key_remove", TimeoutSeconds: 60}
	approval, err := s.plan(sc, req)
	if err != nil {
		s.mu.Unlock()
		return x, err
	}
	req.ExpectedPlanHash = approval.PlanHash
	x.State = "remove_pending"
	err = s.saveInstallation(x)
	s.mu.Unlock()
	if err != nil {
		return x, err
	}
	execution, runErr := s.execute(ctx, sc, req, confirm)
	s.mu.Lock()
	defer s.mu.Unlock()
	x.ExecutionID = execution.ID
	x.State = "remove_failed"
	switch execution.State {
	case "completed":
		x.State = "removed"
	case "unknown_outcome":
		x.State = "remove_unknown"
	}
	if err := s.saveInstallation(x); err != nil {
		return x, err
	}
	return x, runErr
}

// RotatePublicKey composes independently approved installation, login and removal. New key
// installation and independent login must finish before old-key removal.
func (s *Service) RotatePublicKey(ctx context.Context, sc Scope, oldID, bootstrapRoute, newIdentityID, newRoute string, confirm ConfirmFunc) (KeyInstallation, error) {
	next, err := s.InstallPublicKey(ctx, sc, bootstrapRoute, newIdentityID, confirm)
	if err != nil || next.State != "installed_unverified" {
		return next, fmt.Errorf("new key installation incomplete")
	}
	next, err = s.VerifyInstallation(ctx, sc, next.ID, newRoute, confirm)
	if err != nil || next.State != "verified" {
		return next, fmt.Errorf("new key login not verified; old key retained")
	}
	old, err := s.RemovePublicKey(ctx, sc, oldID, next.ID, newRoute, confirm)
	if err != nil || old.State != "removed" {
		return next, fmt.Errorf("new key verified; old key removal incomplete")
	}
	return next, nil
}

// ReconcilePublicKey observes an ambiguous installation/removal without replaying
// a mutation. An absent exact managed entry is distinguished from inspection
// errors by its dedicated exit status. It never claims authentication succeeded.
func (s *Service) ReconcilePublicKey(ctx context.Context, sc Scope, id, routeID string, confirm ConfirmFunc) (KeyInstallation, error) {
	s.mu.Lock()
	x, ok := s.data.Installations[id]
	server, identity, err := s.keyRoute(sc, routeID)
	if !ok || x.OwnerID != sc.OwnerID || err != nil || x.ServerID != server.ID || x.Username != identity.Username || x.State == "install_pending" || x.State == "verification_pending" || x.State == "remove_pending" {
		s.mu.Unlock()
		return KeyInstallation{}, ErrDenied
	}
	script, err := managedKeyInspectionScript(x.PublicKey, x.ID)
	if err != nil {
		s.mu.Unlock()
		return x, err
	}
	capability := "key_install"
	if x.State == "remove_unknown" || x.State == "remove_failed" || x.State == "removed" {
		capability = "key_remove"
	}
	req := ExecuteRequest{RouteID: routeID, Command: script, Capability: capability, TimeoutSeconds: 60}
	approval, err := s.plan(sc, req)
	if err != nil {
		s.mu.Unlock()
		return x, err
	}
	req.ExpectedPlanHash = approval.PlanHash
	priorState := x.State
	s.mu.Unlock()
	execution, runErr := s.execute(ctx, sc, req, confirm)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.data.Installations[id]
	if !exists || current.State != priorState || current.ExecutionID != x.ExecutionID {
		return current, errors.New("installation changed during reconciliation")
	}
	x.ExecutionID = execution.ID
	if execution.ExitCode != nil {
		switch *execution.ExitCode {
		case 0:
			x.State = "installed_unverified"
			x.VerifiedAt = time.Time{}
		case 3:
			x.State = "removed"
			x.VerifiedAt = time.Time{}
		}
	}
	if err := s.saveInstallation(x); err != nil {
		return x, err
	}
	return x, runErr
}

func managedKeyInspectionScript(key, id string) (string, error) {
	if _, err := managedKeyScript(key, id, false); err != nil {
		return "", err
	}
	parsed, _, options, rest, parseErr := ssh.ParseAuthorizedKey([]byte(key))
	if parseErr != nil || len(options) != 0 || len(rest) != 0 {
		return "", errors.New("invalid installation public key")
	}
	canonical := string(ssh.MarshalAuthorizedKey(parsed))
	canonical = canonical[:len(canonical)-1] + " iulita:" + id + "\n"
	script := `import os, stat, pwd, base64
line = base64.b64decode('` + base64.StdEncoding.EncodeToString([]byte(canonical)) + `')
uid = os.getuid()
home = pwd.getpwuid(uid).pw_dir
assert os.path.isabs(home) and os.path.realpath(home) == home, 'unsafe home'
h = os.stat(home, follow_symlinks=False)
assert h.st_uid == uid and not h.st_mode & 0o022, 'unsafe home permissions'
hfd = os.open(home, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
try:
    try: dfd = os.open('.ssh', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=hfd)
    except FileNotFoundError: raise SystemExit(3)
finally: os.close(hfd)
try:
    d = os.fstat(dfd)
    assert d.st_uid == uid and not d.st_mode & 0o077, 'unsafe .ssh permissions'
    try: fd = os.open('authorized_keys', os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dfd)
    except FileNotFoundError: raise SystemExit(3)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        assert stat.S_ISREG(info.st_mode) and info.st_uid == uid and info.st_nlink == 1 and not info.st_mode & 0o077, 'unsafe authorized_keys'
        assert info.st_size <= 1048576, 'authorized_keys too large'
        data = source.read(1048577)
        assert len(data) <= 1048576, 'authorized_keys too large'
    lines = data.splitlines(keepends=True)
    marker = line.split()[-1]
    matches = [item for item in lines if marker in item.split()]
    assert not matches or matches == [line], 'managed marker conflict'
    raise SystemExit(0 if matches else 3)
finally: os.close(dfd)
`
	return "python3 - <<'IULITA_MANAGED_KEY_INSPECTION'\n" + script + "IULITA_MANAGED_KEY_INSPECTION\n", nil
}
