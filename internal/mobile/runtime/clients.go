package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
)

// ClientOperation is an explicit offline registration operation. It starts no listener.
type ClientOperation struct {
	Action, ClientID, SecretPath string
	Registration                 oauthstore.ClientRegistration
}

// ManageClients serializes offline OAuth policy changes with the running realm.
// Service credentials are generated once into a new private file, never returned.
func ManageClients(ctx context.Context, settings Settings, operation ClientOperation) ([]oauthstore.ClientRegistration, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	lock, err := oauthstore.LockRealm(settings.RealmDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close() //nolint:errcheck // Preserve operation error.
	realm, err := oauthstore.LoadRealm(settings.RealmDir, settings.Issuer, settings.Audience)
	if err != nil {
		return nil, err
	}
	store, err := oauthstore.Open(ctx, settings.OAuthDB, realm)
	if err != nil {
		return nil, err
	}
	defer store.Close() //nolint:errcheck // Preserve operation error.
	switch operation.Action {
	case "list":
		return store.ListOAuthClients(ctx)
	case "disable":
		if operation.ClientID == settings.ClientID {
			return nil, errors.New("primary native registration is required by runtime")
		}
		return nil, store.ChangeOAuthClient(ctx, operation.ClientID, "", true)
	case "register":
		if operation.Registration.Kind == "service" {
			accounts, openErr := sqlitedb.Open(ctx, settings.AccountDB, false)
			if openErr != nil {
				return nil, openErr
			}
			var count int
			checkErr := accounts.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=? AND disabled=0 AND must_change_pass=0 AND password_hash<>''`, operation.Registration.OwnerID).Scan(&count)
			accounts.Close() //nolint:errcheck,gosec // Preserve account validation error.
			if checkErr != nil || count != 1 {
				return nil, errors.New("service owner must be an available password account")
			}
		}
	case "rotate-secret":
		registration, policyErr := store.ClientPolicy(ctx, operation.ClientID)
		if policyErr != nil || registration.Kind != "service" {
			return nil, oauthstore.ErrInvalid
		}
	default:
		return nil, oauthstore.ErrInvalid
	}
	needsSecret := operation.Action == "rotate-secret" || operation.Registration.Kind == "service"
	secret := ""
	change := func() error {
		if operation.Action == "register" {
			return store.RegisterOAuthClient(ctx, operation.Registration, secret)
		}
		return store.ChangeOAuthClient(ctx, operation.ClientID, secret, false)
	}
	if needsSecret {
		if !filepath.IsAbs(operation.SecretPath) || filepath.Clean(operation.SecretPath) != operation.SecretPath {
			return nil, errors.New("new absolute secret output path required")
		}
		secret, err = oauthstore.Secret()
		if err != nil {
			return nil, err
		}
		return nil, withDurableClientSecret(operation.SecretPath, secret, syncSecretDirectory, change)
	} else if operation.SecretPath != "" {
		return nil, oauthstore.ErrInvalid
	}
	return nil, change()
}

// Persist both the secret contents and its directory entry before committing a
// credential hash. Otherwise a crash can lose the only copy of a rotated secret.
// Dependencies are local to this operation so failure tests need no global hooks.
func withDurableClientSecret(path, secret string, syncDirectory func(string) error, change func() error) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // Absolute offline operator path; exclusive creation prevents replacing an existing secret.
	if err != nil {
		return err
	}
	if _, err = output.WriteString(secret + "\n"); err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(path))
	}
	if err == nil {
		err = change()
	}
	if err != nil {
		os.Remove(path) //nolint:errcheck,gosec // Only this operation's newly created secret file is removed.
	}
	return err
}

func syncSecretDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // Parent directory of the explicit offline operator output, never remote input.
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
