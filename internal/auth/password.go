package auth

import (
	"context"
	"fmt"

	"github.com/iulita-ai/iulita/internal/domain"
	"golang.org/x/crypto/bcrypt"
)

// PasswordUserReader looks up the account record used for password verification.
type PasswordUserReader interface {
	GetUserByUsername(context.Context, string) (*domain.User, error)
}

// VerifyPasswordCredentials shares the existing account/bcrypt verification
// without issuing or accepting any dashboard token. MustChangePass remains a
// caller policy: legacy login can reach password change, mobile login cannot.
func VerifyPasswordCredentials(ctx context.Context, users PasswordUserReader, username, password string) (*domain.User, error) {
	user, err := users.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	if user == nil {
		// A fixed dummy hash reduces username enumeration through bcrypt timing.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"), []byte(password)) //nolint:errcheck // Timing equalization always rejects this missing account.
		return nil, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil || user.Disabled {
		return nil, ErrInvalidCredentials
	}
	return user, nil
}
