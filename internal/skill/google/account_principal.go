package google

import (
	"context"
	"errors"
	"net/http"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
)

type accountPrincipalKey struct{}

// WithAccountPrincipal selects stored credentials belonging to the verified
// account only. It disables env/config/ADC fallbacks for this execution; it is
// not a legacy namespace opt-out and grants no authority by itself.
func WithAccountPrincipal(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, accountPrincipalKey{}, owner)
}

func (c *Client) ownedTokenSource(ctx context.Context, owner, userID, alias string) (oauth2.TokenSource, error) {
	if owner == "" || userID != owner {
		return nil, errors.New("google account principal mismatch")
	}
	a, err := c.resolveAccount(ctx, owner, alias)
	if err != nil {
		return nil, err
	}
	if a == nil || a.UserID != owner {
		return nil, errors.New("owned google account unavailable")
	}
	return c.tokenSource(ctx, a)
}

func (c *Client) ownedAccountsAvailable(ctx context.Context, owner string) bool {
	if c == nil || c.store == nil || owner == "" {
		return false
	}
	accounts, err := c.store.ListGoogleAccounts(ctx, owner)
	if err != nil {
		return false
	}
	for i := range accounts {
		if accounts[i].UserID == owner {
			return true
		}
	}
	return false
}

type accountTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

// RoundTrip binds account requests to the owning turn lifetime.
func (t accountTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(request.Clone(t.ctx))
}

func apiOptions(ctx context.Context, source oauth2.TokenSource) []option.ClientOption {
	if _, scoped := ctx.Value(accountPrincipalKey{}).(string); scoped {
		client := oauth2.NewClient(ctx, source)
		// Generated API calls without an explicit .Context still inherit the
		// native turn's cancellation/deadline. No detached request after Stop.
		client.Transport = accountTransport{ctx: ctx, base: client.Transport}
		return []option.ClientOption{option.WithHTTPClient(client)}
	}
	return []option.ClientOption{option.WithTokenSource(source)}
}
