package telegrambusiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
)

func newMediaTestVault(t *testing.T) (*MediaVault, Scope, MediaAuthority) {
	t.Helper()
	crypto, err := config.NewEncryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := NewMediaVault(filepath.Join(t.TempDir(), "vault"), crypto)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := vault.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	scope := Scope{Key: Key{InstanceID: "instance", BotID: 202, ConnectionID: "connection"}, OwnerID: "owner", PeerID: 303}
	return vault, scope, MediaAuthority{Epoch: 1, PeerRevision: 1, OwnerSecurityEpoch: 1, RuntimeGeneration: "runtime", RestoreGeneration: "restore"}
}

func TestBusinessMediaCipherScopeReferenceAndTTL(t *testing.T) {
	vault, scope, authority := newMediaTestVault(t)
	ctx := context.Background()
	now := time.Now().UTC()
	vault.now = func() time.Time { return now }
	const private = "private-owner-upload-only-ciphertext-fixture"
	ref, err := vault.Put(ctx, scope, "fixture.txt", "text/plain", []byte(private), authority)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(vault.root, ref))
	if err != nil || bytes.Contains(stored, []byte(private)) || bytes.Contains(stored, []byte("filename")) {
		t.Fatalf("plaintext upload on disk: %v", err)
	}
	info, err := os.Stat(filepath.Join(vault.root, ref))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private file mode: %v", err)
	}
	blob, err := vault.Read(ctx, scope, ref)
	if err != nil || string(blob.Data) != private || blob.Authority != authority {
		t.Fatalf("valid scope read: %v", err)
	}
	foreign := scope
	foreign.PeerID++
	if _, checkErr1 := vault.Read(ctx, foreign, ref); !errors.Is(checkErr1, ErrDenied) {
		t.Fatalf("cross-peer read: %v", checkErr1)
	}
	foreign = scope
	foreign.OwnerID = "another-owner"
	if _, checkErr2 := vault.Read(ctx, foreign, ref); !errors.Is(checkErr2, ErrDenied) {
		t.Fatalf("cross-owner read: %v", checkErr2)
	}
	second, err := vault.Put(ctx, scope, "second.txt", "text/plain", []byte("second fixture"), authority)
	if err != nil {
		t.Fatal(err)
	}
	if checkErr3 := os.WriteFile(filepath.Join(vault.root, second), stored, 0o600); checkErr3 != nil {
		t.Fatal(checkErr3)
	}
	if _, checkErr4 := vault.Read(ctx, scope, second); !errors.Is(checkErr4, ErrDenied) {
		t.Fatalf("ciphertext ref substitution: %v", checkErr4)
	}
	now = now.Add(2 * time.Hour)
	if _, checkErr5 := vault.Read(ctx, scope, ref); !errors.Is(checkErr5, ErrDenied) {
		t.Fatalf("expired read: %v", checkErr5)
	}
	if checkErr6 := vault.Delete(ctx, foreign, ref); !errors.Is(checkErr6, ErrDenied) {
		t.Fatalf("foreign expired delete: %v", checkErr6)
	}
	if checkErr7 := vault.Delete(ctx, scope, ref); checkErr7 != nil {
		t.Fatalf("expired cleanup: %v", checkErr7)
	}
	if checkErr8 := vault.Delete(ctx, scope, ref); checkErr8 != nil {
		t.Fatalf("repeat cleanup: %v", checkErr8)
	}
}

func TestBusinessMediaRejectsMissingCryptoPathsAndFakeMagic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	if _, checkErr9 := NewMediaVault(root, nil); !errors.Is(checkErr9, ErrUnavailable) {
		t.Fatalf("missing crypto: %v", checkErr9)
	}
	if _, checkErr10 := os.Stat(root); !os.IsNotExist(checkErr10) {
		t.Fatalf("plaintext fallback created directory: %v", checkErr10)
	}
	vault, scope, authority := newMediaTestVault(t)
	for _, tc := range []struct {
		name, mime string
		data       []byte
	}{
		{"../outside.txt", "text/plain", []byte("fixture")},
		{"https://example.invalid/file", "text/plain", []byte("fixture")},
		{"control\x01.txt", "text/plain", []byte("fixture")},
		{"image.png", "image/png", []byte("not png")},
		{"sound.ogg", "audio/ogg", []byte("not ogg")},
		{"sound.mp3", "audio/mpeg", []byte("not mp3")},
		{"movie.mp4", "video/mp4", []byte("not mp4")},
		{"file.pdf", "application/pdf", []byte("not pdf")},
		{"file.txt", "text/plain", []byte{0, 1, 2, 3}},
	} {
		if _, checkErr11 := vault.Put(context.Background(), scope, tc.name, tc.mime, tc.data, authority); !errors.Is(checkErr11, ErrDenied) {
			t.Fatalf("invalid %q accepted: %v", tc.mime, checkErr11)
		}
	}
	var imageData bytes.Buffer
	if checkErr12 := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 2))); checkErr12 != nil {
		t.Fatal(checkErr12)
	}
	if _, checkErr13 := vault.Put(context.Background(), scope, "image.png", "image/png", imageData.Bytes(), authority); checkErr13 != nil {
		t.Fatalf("valid PNG rejected: %v", checkErr13)
	}
	if _, checkErr14 := vault.Put(context.Background(), scope, "large.txt", "text/plain", bytes.Repeat([]byte("a"), MaxMediaBytes+1), authority); !errors.Is(checkErr14, ErrQuota) {
		t.Fatalf("oversize: %v", checkErr14)
	}
}

func TestBusinessMediaSymlinksAndPinnedDirectory(t *testing.T) {
	vault, scope, authority := newMediaTestVault(t)
	ctx := context.Background()
	ref, err := vault.Put(ctx, scope, "fixture.txt", "text/plain", []byte("original protected bytes"), authority)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if checkErr15 := os.WriteFile(outside, []byte("outside private fixture"), 0o600); checkErr15 != nil {
		t.Fatal(checkErr15)
	}
	if checkErr16 := os.Remove(filepath.Join(vault.root, ref)); checkErr16 != nil {
		t.Fatal(checkErr16)
	}
	if checkErr17 := os.Symlink(outside, filepath.Join(vault.root, ref)); checkErr17 != nil {
		t.Fatal(checkErr17)
	}
	if _, checkErr18 := vault.Read(ctx, scope, ref); !errors.Is(checkErr18, ErrDenied) {
		t.Fatalf("leaf symlink read: %v", checkErr18)
	}
	if checkErr19 := os.Remove(filepath.Join(vault.root, ref)); checkErr19 != nil {
		t.Fatal(checkErr19)
	}
	ref, err = vault.Put(ctx, scope, "fixture.txt", "text/plain", []byte("original protected bytes"), authority)
	if err != nil {
		t.Fatal(err)
	}
	moved := vault.root + "-moved"
	if checkErr20 := os.Rename(vault.root, moved); checkErr20 != nil {
		t.Fatal(checkErr20)
	}
	if checkErr21 := os.Mkdir(vault.root, 0o700); checkErr21 != nil {
		t.Fatal(checkErr21)
	}
	if checkErr22 := os.WriteFile(filepath.Join(vault.root, ref), []byte("replacement path"), 0o600); checkErr22 != nil {
		t.Fatal(checkErr22)
	}
	blob, err := vault.Read(ctx, scope, ref)
	if err != nil || string(blob.Data) != "original protected bytes" {
		t.Fatalf("directory descriptor not pinned: %v", err)
	}
	if _, checkErr23 := vault.Put(ctx, scope, "new.txt", "text/plain", []byte("fixture"), authority); !errors.Is(checkErr23, ErrDenied) {
		t.Fatalf("replacement root write: %v", checkErr23)
	}
}

func TestBusinessMediaLockSymlinkRejectedWithoutChmodTarget(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if checkErr1 := os.WriteFile(outside, []byte("outside fixture"), 0o644); checkErr1 != nil {
		t.Fatal(checkErr1)
	}
	if checkErr2 := os.Symlink(outside, filepath.Join(root, ".quota.lock")); checkErr2 != nil {
		t.Fatal(checkErr2)
	}
	crypto, err := config.NewEncryptor(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr3 := NewMediaVault(root, crypto); !errors.Is(checkErr3, ErrDenied) {
		t.Fatalf("symlink lock accepted: %v", checkErr3)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("lock target mode changed: %v", err)
	}
}

func TestBusinessMediaScopePurgeAndQuota(t *testing.T) {
	vault, scope, authority := newMediaTestVault(t)
	ctx := context.Background()
	first, err := vault.Put(ctx, scope, "first.txt", "text/plain", []byte("first"), authority)
	if err != nil {
		t.Fatal(err)
	}
	other := scope
	other.PeerID++
	second, err := vault.Put(ctx, other, "second.txt", "text/plain", []byte("second"), authority)
	if err != nil {
		t.Fatal(err)
	}
	if checkErr24 := vault.PurgeScope(ctx, scope); checkErr24 != nil {
		t.Fatal(checkErr24)
	}
	if _, checkErr25 := vault.Read(ctx, scope, first); !errors.Is(checkErr25, ErrDenied) {
		t.Fatalf("purge retained peer: %v", checkErr25)
	}
	if _, checkErr26 := vault.Read(ctx, other, second); checkErr26 != nil {
		t.Fatalf("purge crossed peer: %v", checkErr26)
	}
	fixture := mediaScopePrefix(scope) + strings.Repeat("a", 32)
	file, err := vault.files.OpenFile(fixture, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if checkErr27 := file.Truncate(50 << 20); checkErr27 != nil {
		file.Close()
		t.Fatal(checkErr27)
	}
	if checkErr28 := file.Close(); checkErr28 != nil {
		t.Fatal(checkErr28)
	}
	if _, checkErr29 := vault.Put(ctx, scope, "over.txt", "text/plain", []byte("fixture"), authority); !errors.Is(checkErr29, ErrQuota) {
		t.Fatalf("connection quota bypassed: %v", checkErr29)
	}
}

func TestBusinessMediaServiceEpochAndKnownUpload(t *testing.T) {
	f := setup(t, false)
	vault, _, _ := newMediaTestVault(t)
	vault.now = func() time.Time { return f.now }
	f.service.options.Media = vault
	if checkErr30 := f.repo.Update(context.Background(), f.scope.Key, func(state *State) error {
		state.Connection.OwnerSecurityEpoch = 1
		state.Peers[f.scope.PeerID].Policy.AllowedMethods = append(state.Peers[f.scope.PeerID].Policy.AllowedMethods, SendDocument, SendPhoto)
		return nil
	}); checkErr30 != nil {
		t.Fatal(checkErr30)
	}
	st, err := f.repo.Get(context.Background(), f.scope.Key)
	if err != nil {
		t.Fatal(err)
	}
	authority := MediaAuthorityFor(st.Connection, st.Peers[f.scope.PeerID])
	ref, err := vault.Put(context.Background(), f.scope, "fixture.txt", "text/plain", []byte("scoped owner upload"), authority)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendDocument, Params: Params{"document": encoded}})
	if err != nil || action == nil {
		t.Fatalf("known upload prepare: %v", err)
	}
	if f.api.calls.Load() != 0 {
		t.Fatal("preparation sent Telegram request")
	}
	if _, checkErr4 := f.service.Prepare(context.Background(), PrepareInput{Scope: f.scope, Method: SendPhoto, Params: Params{"photo": encoded}}); !errors.Is(checkErr4, ErrDenied) {
		t.Fatalf("document accepted as photo: %v", checkErr4)
	}
	if _, checkErr31 := f.service.ReadMedia(context.Background(), f.scope, ref); checkErr31 != nil {
		t.Fatalf("known upload read: %v", checkErr31)
	}
	if checkErr32 := f.repo.Update(context.Background(), f.scope.Key, func(state *State) error { state.Connection.Epoch++; return nil }); checkErr32 != nil {
		t.Fatal(checkErr32)
	}
	if _, checkErr33 := f.service.ReadMedia(context.Background(), f.scope, ref); !errors.Is(checkErr33, ErrStale) {
		t.Fatalf("stale artifact authority accepted: %v", checkErr33)
	}
}
