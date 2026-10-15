package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

func TestHTTPStandingRoutesScopeAndRevoke(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(filepath.Join(t.TempDir(), "standing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	grants := make([]domain.HTTPStandingGrant, 2)
	for i, owner := range []string{"owner", "foreign"} {
		grants[i] = domain.HTTPStandingGrant{OwnerUserID: owner, Scope: domain.HTTPStandingScopeOrigin, Scheme: "https", Host: "example.test", Port: "443", ExpiresAt: time.Now().Add(time.Hour)}
		if err = store.SaveHTTPStandingGrant(ctx, &grants[i]); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Config{Store: store, StandingGrants: store, AuthService: auth.NewService(store, testJWTSecret, time.Hour, time.Hour), Logger: zap.NewNop(), StaticFS: fstest.MapFS{"index.html": {Data: []byte("SPA_FALLBACK_CANARY")}}})
	token := func(owner string, role domain.UserRole) string {
		t.Helper()
		signed, signErr := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{UserID: owner, Role: role, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(testJWTSecret))
		if signErr != nil {
			t.Fatal(signErr)
		}
		return signed
	}
	ownerToken, adminToken := token("owner", domain.RoleRegular), token("admin", domain.RoleAdmin)
	request := func(method, path, credential string, expected int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, http.NoBody)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		response, testErr := server.app.Test(req)
		if testErr != nil {
			t.Fatal(testErr)
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(response.Body)
		if readErr != nil || response.StatusCode != expected {
			t.Fatalf("%s %s: status=%d read=%v", method, path, response.StatusCode, readErr)
		}
		if expected == http.StatusOK && !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
			t.Fatal("standing grant API returned the SPA instead of JSON")
		}
		return body
	}
	request("GET", "/api/http-standing-grants", "", http.StatusUnauthorized)
	body := request("GET", "/api/http-standing-grants?owner=foreign", ownerToken, http.StatusOK)
	var listed []struct{ ID int64 }
	if err = json.Unmarshal(body, &listed); err != nil || len(listed) != 1 || listed[0].ID != grants[0].ID {
		t.Fatal("regular user selected a foreign owner's approvals", err)
	}
	foreignPath := "/api/http-standing-grants/" + strconv.FormatInt(grants[1].ID, 10)
	request("DELETE", foreignPath, ownerToken, http.StatusNotFound)
	if valid, checkErr := store.HasHTTPStandingGrant(ctx, "foreign", "https", "example.test", "443"); checkErr != nil || !valid {
		t.Fatal("foreign approval changed after rejected revocation", checkErr)
	}
	request("DELETE", "/api/http-standing-grants/"+strconv.FormatInt(grants[0].ID, 10), ownerToken, http.StatusNoContent)
	if valid, checkErr := store.HasHTTPStandingGrant(ctx, "owner", "https", "example.test", "443"); checkErr != nil || valid {
		t.Fatal("own revoked approval remains effective", checkErr)
	}
	body = request("GET", "/api/http-standing-grants?owner=foreign", adminToken, http.StatusOK)
	if err = json.Unmarshal(body, &listed); err != nil || len(listed) != 1 || listed[0].ID != grants[1].ID {
		t.Fatal("admin could not inspect selected owner's approvals", err)
	}
	request("DELETE", foreignPath, adminToken, http.StatusNoContent)
	if valid, checkErr := store.HasHTTPStandingGrant(ctx, "foreign", "https", "example.test", "443"); checkErr != nil || valid {
		t.Fatal("admin revoked approval remains effective", checkErr)
	}
}
