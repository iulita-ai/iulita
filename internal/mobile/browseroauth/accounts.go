package browseroauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

// Account Accounts is the live account source, independently of browser cookies or JWTs.
type Account struct {
	ID, DisplayName, CredentialVersion string
	Disabled, MustChangePassword       bool
}

// Accounts authenticates accounts and guards their current security state.
type Accounts interface {
	Authenticate(context.Context, string, string) (Account, error)
	Lookup(context.Context, string) (Account, error)
	Guard(context.Context, string, func(Account) error) error
}

// UserReader reads account records by identity.
type UserReader interface {
	auth.PasswordUserReader
	GetUser(context.Context, string) (*domain.User, error)
	WithUserSecurity(context.Context, string, func(*domain.User) error) error
}

// PasswordAccounts adapts password accounts to the independent mobile OAuth realm.
type PasswordAccounts struct{ Users UserReader }

func account(u *domain.User) Account {
	stamp := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%t\x00%t\x00%d", u.PasswordHash, u.Disabled, u.MustChangePass, u.SecurityEpoch)))
	return Account{ID: u.ID, DisplayName: u.DisplayName, CredentialVersion: hex.EncodeToString(stamp[:]), Disabled: u.Disabled, MustChangePassword: u.MustChangePass}
}

// Authenticate verifies credentials and returns current account security state.
func (a PasswordAccounts) Authenticate(ctx context.Context, username, password string) (Account, error) {
	u, err := auth.VerifyPasswordCredentials(ctx, a.Users, username, password)
	if err != nil {
		return Account{}, err
	}
	return account(u), nil
}

// Lookup looks up current account security state.
func (a PasswordAccounts) Lookup(ctx context.Context, id string) (Account, error) {
	u, err := a.Users.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && u == nil) {
		return Account{}, auth.ErrUserNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return account(u), nil
}

// Guard holds account authority stable while the supplied operation runs.
func (a PasswordAccounts) Guard(ctx context.Context, id string, fn func(Account) error) error {
	return a.Users.WithUserSecurity(ctx, id, func(u *domain.User) error {
		if u == nil {
			return auth.ErrUserNotFound
		}
		return fn(account(u))
	})
}
