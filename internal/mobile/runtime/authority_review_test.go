package runtime

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

type failingLookupAccounts struct {
	browseroauth.Accounts
	err error
}

func (a failingLookupAccounts) Lookup(context.Context, string) (browseroauth.Account, error) {
	return browseroauth.Account{}, a.err
}
func TestExecutionAuthorityPreservesOperationalFailures(t *testing.T) {
	temporary := errors.New("account storage busy")
	for _, scenario := range []struct {
		err     error
		revoked bool
	}{{temporary, false}, {auth.ErrUserNotFound, true}, {sql.ErrNoRows, true}} {
		err := verifyExecutionAuthority(context.Background(), failingLookupAccounts{err: scenario.err}, nil, engine.Authority{OwnerID: "owner"})
		if scenario.revoked {
			if !errors.Is(err, oauthstore.ErrInvalid) {
				t.Fatal(err)
			}
		} else if !errors.Is(err, temporary) || errors.Is(err, oauthstore.ErrInvalid) {
			t.Fatal("temporary error became permanent revocation", err)
		}
	}
}
