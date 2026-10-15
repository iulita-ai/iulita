package oauthstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	legacy "github.com/iulita-ai/iulita/internal/storage/sqlite"
)

func TestOfflineRotationRevokesAndDoesNotReplayRestoredQueue(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	tokens := issue(t, e)
	mainPath := filepath.Join(t.TempDir(), "main.sqlite")
	main, err := legacy.New(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	if err = main.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = main.CreateUser(ctx, &domain.User{ID: f.owner, Username: "owner", PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	signer, _ := f.realm.CursorSigner()
	chats, err := conversations.OpenExisting(ctx, mainPath, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer chats.Close()
	c, err := chats.Create(ctx, f.owner, "restored", conversations.Settings{ProfileID: "default", MemoryMode: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := chats.Accept(ctx, f.owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "must never replay after restore"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := LockRealm(filepath.Join(f.dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if err = RotateOffline(ctx, filepath.Join(f.dir, "keys"), f.path, mainPath, issuer, audience, true, false); !errors.Is(err, ErrConflict) {
		t.Fatal("live runtime lock ignored", err)
	}
	lock.Close()
	if err = RotateOffline(ctx, filepath.Join(f.dir, "keys"), f.path, mainPath, issuer, audience, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err = e.exchange(refreshForm(refresh(t, tokens))); err == nil {
		t.Fatal("old live store continued issuing")
	}
	realm, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	if realm.epoch <= f.realm.epoch || realm.kid == f.realm.kid {
		t.Fatal("signing epoch did not change")
	}
	reopened, err := Open(ctx, f.path, realm)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := chats.GetTurn(ctx, f.owner, c.ID, turn.ID)
	if err != nil || got.State != "cancelled" || !got.CancellationRequested {
		t.Fatal("restored queue retained execution authority", got, err)
	}
	family, err := reopened.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("family survived restore")
	}
	if err = os.WriteFile(filepath.Join(f.dir, "keys", pendingFile), []byte(`{"Epoch":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = RotateOffline(ctx, filepath.Join(f.dir, "keys"), f.path, mainPath, issuer, audience, true, true); err != nil {
		t.Fatal("explicit interrupted recovery", err)
	}
	recovered, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil || recovered.epoch <= realm.epoch {
		t.Fatal("recovery did not publish a new epoch", err)
	}
	if _, err = os.Stat(filepath.Join(f.dir, "keys", pendingFile)); !os.IsNotExist(err) {
		t.Fatal("completed recovery retained marker", err)
	}
}
func TestInterruptedRotationBlocksStartupUntilExplicitRecovery(t *testing.T) {
	f := fixture(t, Pending)
	dir := filepath.Join(f.dir, "keys")
	if err := os.WriteFile(filepath.Join(dir, pendingFile), []byte(`{"Epoch":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRealm(dir, issuer, audience); err == nil {
		t.Fatal("interrupted maintenance silently started")
	}
	if _, err := InitializeRealm(dir, issuer, audience); err == nil {
		t.Fatal("initialization bypassed maintenance marker")
	}
}

func TestActualOlderDatabaseRestoreRequiresOfflineInvalidation(t *testing.T) {
	f := fixture(t, Active)
	e := newEngine(f)
	tokens := issue(t, e)
	mainPath := filepath.Join(t.TempDir(), "main.sqlite")
	main, err := legacy.New(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = main.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = main.CreateUser(ctx, &domain.User{ID: f.owner, Username: "owner", PasswordHash: "fixture"}); err != nil {
		t.Fatal(err)
	}
	signer, _ := f.realm.CursorSigner()
	chats, err := conversations.OpenExisting(ctx, mainPath, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := chats.Create(ctx, f.owner, "snapshot", conversations.Settings{ProfileID: "default", MemoryMode: "isolated"})
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := chats.Accept(ctx, f.owner, c.ID, conversations.Input{ClientMessageID: uuid.NewString(), Generation: 1, Text: "queued before backup"})
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	oauthBackup, mainBackup := filepath.Join(backupDir, "oauth.sqlite"), filepath.Join(backupDir, "main.sqlite")
	accountDB, err := sqlitedb.Open(ctx, mainPath, false)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise a real pre-target v3 backup as well as the current v4 database
	// used by TestOfflineRotationRevokesAndDoesNotReplayRestoredQueue.
	for _, query := range []string{`ALTER TABLE mobile_turns DROP COLUMN target_device_id`, `DROP TABLE mobile_chat_schema`, `CREATE TABLE mobile_chat_schema(version INTEGER PRIMARY KEY CHECK(version=3))`, `INSERT INTO mobile_chat_schema VALUES(3)`} {
		if _, err = accountDB.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = accountDB.ExecContext(ctx, `VACUUM INTO ?`, mainBackup); err != nil {
		t.Fatal(err)
	}
	accountDB.Close()
	if _, err = f.db.ExecContext(ctx, `VACUUM INTO ?`, oauthBackup); err != nil {
		t.Fatal(err)
	}
	if err = RotateOffline(ctx, filepath.Join(f.dir, "keys"), f.path, mainPath, issuer, audience, false, false); err != nil {
		t.Fatal(err)
	}
	current, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	chats.Close()
	main.Close()
	f.Close()
	for _, pair := range [][2]string{{oauthBackup, f.path}, {mainBackup, mainPath}} {
		body, stepErr1 := os.ReadFile(pair[0])
		if stepErr1 != nil {
			t.Fatal(stepErr1)
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if stepErr1 = os.Remove(pair[1] + suffix); stepErr1 != nil && !os.IsNotExist(stepErr1) {
				t.Fatal(stepErr1)
			}
		}
		if stepErr1 = os.WriteFile(pair[1], body, 0o600); stepErr1 != nil {
			t.Fatal(stepErr1)
		}
	}
	if opened, stepErr2 := Open(ctx, f.path, current); stepErr2 == nil {
		opened.Close()
		t.Fatal("older database accepted current keys")
	}
	if err = RotateOffline(ctx, filepath.Join(f.dir, "keys"), f.path, mainPath, issuer, audience, true, false); err != nil {
		t.Fatal("explicit restored snapshot invalidation", err)
	}
	restoredRealm, err := LoadRealm(filepath.Join(f.dir, "keys"), issuer, audience)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, f.path, restoredRealm)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.ValidateAccess(ctx, tokens.GetAccessToken()); err == nil {
		t.Fatal("restored old access resurrected")
	}
	family, err := restored.Family(ctx, f.owner, f.family.ID)
	if err != nil || !family.Revoked {
		t.Fatal("restored refresh family resurrected", err)
	}
	signer, _ = restoredRealm.CursorSigner()
	reopened, err := conversations.OpenExisting(ctx, mainPath, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetTurn(ctx, f.owner, c.ID, turn.ID)
	if err != nil || got.State != "cancelled" || !got.CancellationRequested {
		t.Fatal("restored queue replay authority", got, err)
	}
}
