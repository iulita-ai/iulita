package oauthstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
)

var ctx = context.Background()

const issuer = "https://mobile.example.test"
const audience = "iulita-mobile"
const redirect = "https://mobile.example.test/oauth/callback"

type fixtureData struct {
	*Store
	dir, path, owner, other, device string
	family                          Family
}

func fixture(t *testing.T, stage Stage) *fixtureData {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "realm")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	realm, err := InitializeRealm(filepath.Join(dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatalf("initialize keys: %v", err)
	}
	path := filepath.Join(dir, "oauth.sqlite")
	s, err := Initialize(ctx, path, realm)
	if err != nil {
		t.Fatalf("initialize DB: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	owner, other, device := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if err = s.EnsureOwner(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"mobile", "other"} {
		if err = s.RegisterClient(ctx, fosite.DefaultClient{ID: id, Public: true, RedirectURIs: []string{redirect}, GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scopes: []string{"chats:read"}, Audience: []string{audience}}); err != nil {
			t.Fatal(err)
		}
	}
	thumb := make([]byte, 32)
	if _, err = rand.Read(thumb); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterDevice(ctx, owner, device, base64.RawURLEncoding.EncodeToString(thumb)); err != nil {
		t.Fatal(err)
	}
	f := Family{ID: uuid.NewString(), OwnerID: owner, DeviceID: device, ClientID: "mobile", Stage: stage, AuthTime: time.Now().UTC().Truncate(time.Second), ExpiresAt: time.Now().UTC().Add(20 * 24 * time.Hour), ConsentScopes: []string{"chats:read"}}
	if stage != Active {
		f.Deadline = time.Now().UTC().Add(stage.ttl()).Truncate(time.Second)
	}
	if err = s.CreateFamily(ctx, f); err != nil {
		t.Fatal(err)
	}
	f, err = s.Family(ctx, owner, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &fixtureData{s, dir, path, owner, other, device, f}
}
func requestFor(t *testing.T, f *fixtureData) *fosite.Request {
	t.Helper()
	session, err := f.NewSession(ctx, f.owner, f.family.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session.SetExpiresAt(fosite.AuthorizeCode, now.Add(time.Minute))
	session.SetExpiresAt(fosite.AccessToken, now.Add(time.Minute))
	session.SetExpiresAt(fosite.RefreshToken, now.Add(2*time.Minute))
	c, err := f.GetClient(ctx, "mobile")
	if err != nil {
		t.Fatal(err)
	}
	scopes := fosite.Arguments{}
	if f.family.Stage == Active {
		scopes = fosite.Arguments{"chats:read"}
	}
	return &fosite.Request{ID: f.family.ID, RequestedAt: now, Client: c, GrantedScope: scopes, RequestedScope: fosite.Arguments{"chats:read"}, Session: session}
}
func TestPersistedEpochsOwnershipAndKeys(t *testing.T) {
	f := fixture(t, Active)
	if _, err := f.Family(ctx, f.other, f.family.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.Device(ctx, f.other, f.device); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := f.RevokeFamily(ctx, f.other, f.family.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := f.CreateFamily(ctx, Family{ID: uuid.NewString(), OwnerID: f.other, DeviceID: f.device, ClientID: "mobile", Stage: Active, AuthTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Fatal("foreign device family")
	}
	if err := f.BumpOwnerEpoch(ctx, f.owner, 2); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := f.BumpOwnerEpoch(ctx, f.owner, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	realm, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, f.path, realm)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	family, err := s.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatalf("restart lost revocation: %#v %v", family, err)
	}
	if _, err = s.NewSession(ctx, f.owner, f.family.ID); err == nil {
		t.Fatal("revoked family usable")
	}
	var epoch uint64
	if err = s.db.QueryRow(`SELECT epoch FROM oauth_owners WHERE id=?`, f.owner).Scan(&epoch); err != nil || epoch != 2 {
		t.Fatal("epoch not persistent")
	}
	otherRealm, err := InitializeRealm(filepath.Join(f.dir, "other-keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	if wrong, stepErr1 := Open(ctx, f.path, otherRealm); stepErr1 == nil {
		wrong.Close()
		t.Fatal("mixed realm keys accepted")
	}
	if _, err = Open(ctx, filepath.Join(f.dir, "missing.sqlite"), realm); err == nil {
		t.Fatal("missing DB recreated")
	}
}
func TestNoncesAreOwnedHashedAndOneTime(t *testing.T) {
	f := fixture(t, Pending)
	id := uuid.NewString()
	nonce, err := f.CreateNonce(ctx, f.owner, f.family.ID, id, "activate_device")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.CreateNonce(ctx, f.other, f.family.ID, uuid.NewString(), "activate_device"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = f.CreateNonce(ctx, f.owner, f.family.ID, uuid.NewString(), "recover_device"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = f.ConsumeNonce(ctx, f.owner, f.family.ID, id, "activate_device", nonce); !errors.Is(err, ErrTransaction) {
		t.Fatal(err)
	}
	if err = f.WithRequest(ctx, func(c context.Context) error {
		return f.ConsumeNonce(c, f.owner, f.family.ID, id, "activate_device", "bad")
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = f.WithRequest(ctx, func(c context.Context) error {
		return f.ConsumeNonce(c, f.owner, f.family.ID, id, "activate_device", nonce)
	}); err != nil {
		t.Fatal(err)
	}
	if err = f.WithRequest(ctx, func(c context.Context) error {
		return f.ConsumeNonce(c, f.owner, f.family.ID, id, "activate_device", nonce)
	}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	var hash []byte
	if err = f.db.QueryRow(`SELECT nonce_hash FROM oauth_nonces WHERE id=?`, id).Scan(&hash); err != nil || len(hash) != 32 || string(hash) == nonce {
		t.Fatal("nonce stored raw")
	}
}
func TestMobileJWTRealmAndPersistentValidation(t *testing.T) {
	f := fixture(t, Active)
	r := requestFor(t, f)
	b := r.Session.(*Session).Binding
	now := time.Now().Truncate(time.Second)
	claims := Claims{ClientID: b.ClientID, DeviceID: b.DeviceID, FamilyID: b.FamilyID, Stage: b.Stage, OwnerEpoch: b.OwnerEpoch, DeviceEpoch: b.DeviceEpoch, Scope: "chats:read", AuthTime: b.AuthTime}
	claims.Subject = b.OwnerID
	claims.ID = uuid.NewString()
	claims.IssuedAt = timeJWT(now)
	claims.ExpiresAt = timeJWT(now.Add(time.Minute))
	token, err := f.realm.SignAccess(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.ValidateAccess(ctx, token); err == nil {
		t.Fatal("unrecorded access accepted")
	}
	sig := strings.Split(token, ".")[2]
	if err = f.WithRequest(ctx, func(c context.Context) error { return f.CreateAccessTokenSession(c, sig, r) }); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ValidateAccess(ctx, token); err != nil {
		t.Fatal(err)
	}
	jwksJSON, err := jsonBytes(f.realm.JWKS())
	if err != nil || strings.Contains(string(jwksJSON), "Private") || strings.Contains(string(jwksJSON), "\"d\"") {
		t.Fatal("private material in JWKS")
	}
	// Legacy HS256 JWT with valid-looking mobile fields must fail algorithm/realm checks.
	legacy := legacyToken(t, claims)
	if _, err = f.ValidateAccess(ctx, legacy); err == nil {
		t.Fatal("legacy JWT accepted")
	}
	if err = f.RevokeDevice(ctx, f.owner, f.device, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = f.ValidateAccess(ctx, token); err == nil {
		t.Fatal("revoked device still authorized")
	}
}
func TestPrivateFilesAndFailClosedKeyLoad(t *testing.T) {
	f := fixture(t, Active)
	keyPath := filepath.Join(f.dir, "keys", keyFile)
	for _, path := range []string{keyPath, f.path} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file permissions %s: %v %v", path, info, err)
		}
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRealm(filepath.Dir(keyPath), issuer, audience); !errors.Is(err, ErrKeys) {
		t.Fatal("public key file accepted")
	}
	os.Chmod(keyPath, 0o600)
	if _, err := LoadRealm(filepath.Dir(keyPath), "https://another.example.test", audience); !errors.Is(err, ErrKeys) {
		t.Fatal("wrong issuer")
	}
	if err := os.Rename(keyPath, keyPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRealm(filepath.Dir(keyPath), issuer, audience); !errors.Is(err, ErrKeys) {
		t.Fatal("keys silently recreated")
	}
}
func TestOuterTransactionAndSessionBinding(t *testing.T) {
	f := fixture(t, Active)
	r := requestFor(t, f)
	if err := f.CreateAuthorizeCodeSession(ctx, "sig", r); !errors.Is(err, ErrTransaction) {
		t.Fatal(err)
	}
	abort := errors.New("abort")
	if err := f.WithRequest(ctx, func(c context.Context) error {
		nested, e := f.BeginTX(c)
		if e != nil {
			return e
		}
		if checkErr2 := f.CreateAuthorizeCodeSession(nested, "rolled-back", r); checkErr2 != nil {
			return checkErr2
		}
		if checkErr3 := f.Commit(nested); checkErr3 != nil {
			return checkErr3
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM oauth_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("core commit escaped outer transaction")
	}
	r.Session.(*Session).Binding.OwnerID = f.other
	if err := f.WithRequest(ctx, func(c context.Context) error { return f.CreateAuthorizeCodeSession(c, "bad", r) }); !errors.Is(err, ErrInvalid) {
		t.Fatal("foreign session accepted")
	}
}
func TestConcurrentNonceCAS(t *testing.T) {
	f := fixture(t, Recovery)
	id := uuid.NewString()
	nonce, err := f.CreateNonce(ctx, f.owner, f.family.ID, id, "recover_device")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, f.path, f.realm)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make([]error, 2)
	for i, s := range []*Store{f.Store, other} {
		go func(i int, s *Store) {
			defer wg.Done()
			<-start
			errs[i] = s.WithRequest(ctx, func(c context.Context) error {
				return s.ConsumeNonce(c, f.owner, f.family.ID, id, "recover_device", nonce)
			})
		}(i, s)
	}
	close(start)
	wg.Wait()
	n := 0
	for _, err := range errs {
		if err == nil {
			n++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("nonce winners: %d", n)
	}
}

func TestVerifiedRevocationSurvivesTransportCancellation(t *testing.T) {
	f := fixture(t, Active)
	requestCtx, cancel := context.WithCancel(ctx)
	err := f.WithRequest(requestCtx, func(c context.Context) error {
		root, _ := f.root(c)
		root.verifiedFamily = f.family.ID
		if e := f.RevokeRefreshToken(c, f.family.ID); e != nil {
			return e
		}
		cancel()
		return fosite.ErrInvalidGrant
	})
	if !errors.Is(err, fosite.ErrInvalidGrant) {
		t.Fatal(err)
	}
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatalf("cancel lost security revocation: %#v %v", family, err)
	}
}

func TestDeviceRevokeOperationCannotBeRebound(t *testing.T) {
	f := fixture(t, Active)
	second := uuid.NewString()
	if err := f.RegisterDevice(ctx, f.owner, second, base64.RawURLEncoding.EncodeToString(make([]byte, 32))); err != nil {
		t.Fatal(err)
	}
	op := uuid.NewString()
	for i := 0; i < 2; i++ {
		if err := f.RevokeDeviceOperation(ctx, f.owner, "mobile", op, f.device); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.RevokeDeviceOperation(ctx, f.owner, "mobile", op, second); !errors.Is(err, ErrConflict) {
		t.Fatal("operation rebound", err)
	}
	d, err := f.Device(ctx, f.owner, second)
	if err != nil || d.Revoked {
		t.Fatal("second revocation escaped idempotency", err)
	}
	if err = f.RevokeDeviceOperation(ctx, f.other, "mobile", uuid.NewString(), second); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign device revoked", err)
	}
}
func TestOAuthCleanupPreservesLiveFamilyReplayEvidence(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	tokens := issue(t, e)
	old := refresh(t, tokens)
	if _, err := e.exchange(refreshForm(old)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exchange(refreshForm(old)); err == nil {
		t.Fatal("cleanup discarded refresh replay evidence")
	}
	family, err := f.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("replay did not revoke family", err)
	}
}
