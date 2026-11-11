package oauthstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
)

type upgradeCredentials struct {
	Owner, Other, Device, Family string
	Code, Access, Refresh        string
}

func upgradeEngine(f *fixtureData) *engine {
	strategy := f.Strategy()
	provider := compose.Compose(f.FrameworkConfig(), f.Store, strategy,
		compose.OAuth2AuthorizeExplicitFactory, compose.OAuth2RefreshTokenGrantFactory, compose.OAuth2PKCEFactory)
	return &engine{f, provider, strategy}
}

// TestJOSEUpgradeContinuity is driven by scripts/check_fosite.py upgrade. One
// binary links canonical Fosite/JOSE v3; the second links the patched Fosite/v4.
// They exchange synthetic persisted state, never production credentials.
func TestJOSEUpgradeContinuity(t *testing.T) {
	dir := os.Getenv("IULITA_JOSE_UPGRADE_DIR")
	if dir == "" {
		t.Skip("run python3 scripts/check_fosite.py upgrade for the two-version test")
	}
	switch os.Getenv("IULITA_JOSE_UPGRADE_PHASE") {
	case "issue":
		for _, kind := range []string{"code", "refresh"} {
			f := fixture(t, Active)
			e := upgradeEngine(f)
			c := upgradeCredentials{Owner: f.owner, Other: f.other, Device: f.device, Family: f.family.ID}
			var err error
			if kind == "code" {
				c.Code, err = e.authorize("S256")
			} else {
				response := issue(t, e)
				c.Access, c.Refresh = response.GetAccessToken(), refresh(t, response)
				_, err = f.realm.VerifySignature(c.Access)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, kind)
			if err = os.Mkdir(state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err = os.CopyFS(state, os.DirFS(f.dir)); err != nil {
				t.Fatal(err)
			}
			// CopyFS uses umask defaults; restore strict synthetic realm key modes.
			if err = filepath.WalkDir(f.dir, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				info, statErr := entry.Info()
				if statErr != nil {
					return statErr
				}
				relative, relErr := filepath.Rel(f.dir, path)
				if relErr != nil {
					return relErr
				}
				return os.Chmod(filepath.Join(state, relative), info.Mode().Perm())
			}); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(state, "credentials.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	case "resume":
		for _, kind := range []string{"code", "refresh"} {
			t.Run(kind, func(t *testing.T) {
				state := filepath.Join(dir, kind)
				data, err := os.ReadFile(filepath.Join(state, "credentials.json"))
				if err != nil {
					t.Fatal(err)
				}
				var c upgradeCredentials
				if err = json.Unmarshal(data, &c); err != nil {
					t.Fatal(err)
				}
				realm, err := LoadRealm(filepath.Join(state, "keys"), issuer, audience)
				if err != nil {
					t.Fatal(err)
				}
				store, err := Open(ctx, filepath.Join(state, "oauth.sqlite"), realm)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				family, err := store.Family(ctx, c.Owner, c.Family)
				if err != nil {
					t.Fatal(err)
				}
				f := &fixtureData{store, state, filepath.Join(state, "oauth.sqlite"), c.Owner, c.Other, c.Device, family}
				e := upgradeEngine(f)
				if kind == "code" {
					response, exchangeErr := e.exchange(codeForm(c.Code))
					if exchangeErr != nil {
						t.Fatalf("pre-upgrade code/PKCE state could not be redeemed: %v", exchangeErr)
					}
					if _, err = realm.VerifySignature(response.GetAccessToken()); err != nil {
						t.Fatal("new access token failed validation", err)
					}
					assertActive(t, e, response.GetAccessToken(), true)
					if _, err = e.exchange(codeForm(c.Code)); !errors.Is(err, fosite.ErrInvalidGrant) {
						t.Fatal("pre-upgrade code replay accepted", err)
					}
					assertActive(t, e, response.GetAccessToken(), false)
					return
				}
				if _, err = realm.VerifySignature(c.Access); err != nil {
					t.Fatal("pre-upgrade RS256 access token failed validation", err)
				}
				assertActive(t, e, c.Access, true)
				response, err := e.exchange(refreshForm(c.Refresh))
				if err != nil {
					t.Fatal("pre-upgrade refresh could not rotate", err)
				}
				assertActive(t, e, c.Access, false)
				assertActive(t, e, response.GetAccessToken(), true)
				if _, err = realm.VerifySignature(response.GetAccessToken()); err != nil {
					t.Fatal("rotated RS256 access token failed validation", err)
				}
				if _, err = e.exchange(refreshForm(c.Refresh)); !errors.Is(err, fosite.ErrInvalidGrant) {
					t.Fatal("pre-upgrade refresh reuse accepted", err)
				}
				if _, err = e.exchange(refreshForm(refresh(t, response))); !errors.Is(err, fosite.ErrInvalidGrant) {
					t.Fatal("refresh reuse failed to revoke family", err)
				}
				assertActive(t, e, response.GetAccessToken(), false)
			})
		}
	default:
		t.Fatal("expected issue or resume phase")
	}
}
