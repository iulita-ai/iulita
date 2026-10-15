package runtime

import (
	"context"

	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

// Execution can outlive the RPC. Refresh the account stamp before checking its
// frozen grant. Lookup is deliberately bounded and does not acquire an account
// guard while a conversation transaction may already hold the account DB.
func verifyExecutionAuthority(ctx context.Context, accounts browseroauth.Accounts, store *oauthstore.Store, authority engine.Authority) error {
	account, err := accounts.Lookup(ctx, authority.OwnerID)
	if err != nil || account.ID != authority.OwnerID {
		return oauthstore.ErrInvalid
	}
	available := !account.Disabled && !account.MustChangePassword && account.CredentialVersion != ""
	if syncErr := store.SyncAccount(ctx, account.ID, account.CredentialVersion, available); syncErr != nil {
		return syncErr
	}
	if !available {
		return oauthstore.ErrInvalid
	}
	return store.ValidateAuthority(ctx, authority)
}
