package browseroauth

import (
	"context"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mobile/oauthstore"
)

func TestRestrictedNativeBrowserPKCERefreshAndAuthority(t *testing.T) {
	f := setup(t)
	client := "admin-native"
	scopes := []string{"chats:read", "chats:write", "assistant:services:read", "assistant:services:write"}
	if err := f.store.RegisterOAuthClient(testContext, oauthstore.ClientRegistration{ClientID: client, Name: "Admin assistant", Kind: "native", OwnerID: f.user.ID, Scopes: scopes, RedirectURIs: []string{testRedirect}}, ""); err != nil {
		t.Fatal(err)
	}
	f.s.config.Scopes = scopes
	other := &domain.User{ID: uuid.NewString(), Username: "intruder", PasswordHash: f.user.PasswordHash, DisplayName: "Other"}
	if err := f.users.CreateUser(testContext, other); err != nil {
		t.Fatal(err)
	}
	key, j := installation(t)
	a := f.a
	a.ClientID = client
	a.Scope = "chats:read chats:write assistant:services:read assistant:services:write"
	a.KeyThumbprint = j.Thumbprint()
	w := f.request(authURL(a), nil, nil)
	v := fields(t, w)
	cookie := w.Result().Cookies()[0]
	v.Set("username", other.Username)
	v.Set("password", "correct horse fixture")
	w = f.request("/oauth2/login", v, cookie)
	if w.Code != 400 || f.count(t, "oauth_families") != 0 || w.Header().Get("Location") != "" {
		t.Fatal("foreign native login authorized", w.Code, w.Body.String())
	}
	form := codeExchange(f.code(t, a), j)
	form.Set("client_id", client)
	pending := f.exchange(t, form)
	c, err := f.store.ValidateAccess(testContext, pending.Access)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.FinishInstallation(testContext, c, pending.Challenge.OperationID, pending.Challenge.Nonce, proof(t, key, pending.Challenge), &j, "Synthetic native"); err != nil {
		t.Fatal(err)
	}
	refresh := refreshExchange(pending.Refresh)
	refresh.Set("client_id", client)
	active := f.exchange(t, refresh)
	claims, err := f.store.ValidateAccess(testContext, active.Access)
	if err != nil || claims.Subject != f.user.ID {
		t.Fatal("owned native access", err)
	}
	authority := f.store.Authority(claims)
	if err = f.store.ValidateAuthority(testContext, authority); err != nil {
		t.Fatal("owned native authority", err)
	}
	if !oauthstore.HasScope(claims, "assistant:services:write") {
		t.Fatal("explicit service consent missing")
	}
	// Defense in depth: a retained family that does not satisfy its immutable
	// registration policy must fail every path, even if rows were imported wrong.
	f.exec(t, `UPDATE oauth_client_policy SET owner_id=? WHERE client_id=?`, other.ID, client)
	if _, err = f.store.ValidateAccess(testContext, active.Access); err == nil {
		t.Fatal("foreign retained native access")
	}
	if err = f.store.ValidateAuthority(testContext, authority); err == nil {
		t.Fatal("foreign retained native authority")
	}
	err = f.store.WithRequest(testContext, func(ctx context.Context) error {
		tx, txErr := f.store.Transaction(ctx)
		if txErr != nil {
			return txErr
		}
		_, validateErr := f.store.ValidateIn(ctx, tx, active.Access)
		return validateErr
	})
	if err == nil {
		t.Fatal("foreign retained native transactional access")
	}
	refresh = refreshExchange(active.Refresh)
	refresh.Set("client_id", client)
	w = f.request("/oauth2/token", refresh, nil)
	if w.Code != 400 {
		t.Fatal("foreign retained family refreshed", w.Code, w.Body.String())
	}
	if _, err = f.store.TokenOwner(testContext, "refresh_token", active.Refresh, client, ""); err == nil {
		t.Fatal("foreign token-owner preflight accepted")
	}
	primary, err := f.store.ClientPolicy(testContext, "mobile")
	if err != nil || primary.OwnerID != "" || len(primary.Scopes) != 2 {
		t.Fatal("old registration widened", primary, err)
	}
	// The original multi-account client continues to accept this user's explicit
	// login, without inheriting service scopes from the secondary registration.
	oldA := f.a
	_, oldJ := installation(t)
	oldA.KeyThumbprint = oldJ.Thumbprint()
	w = f.request(authURL(oldA), nil, nil)
	v = fields(t, w)
	cookie = w.Result().Cookies()[0]
	v.Set("username", other.Username)
	v.Set("password", "correct horse fixture")
	w = f.request("/oauth2/login", v, cookie)
	v = fields(t, w)
	cookie = w.Result().Cookies()[0]
	v.Set("decision", "approve")
	w = f.request("/oauth2/consent", v, cookie)
	if w.Code != 303 {
		t.Fatal("multi-account primary consent regressed", w.Code)
	}
	callback, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	old := f.exchange(t, codeExchange(callback.Query().Get("code"), oldJ))
	oldClaims, err := f.store.ValidateAccess(testContext, old.Access)
	if err != nil || oldClaims.Subject != other.ID || oauthstore.HasScope(oldClaims, "assistant:services:read") {
		t.Fatal("multi-account primary authority regressed", err)
	}
}
