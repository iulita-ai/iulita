package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/mcpclient"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
)

type dashboardMCPProtocol struct{}

func (dashboardMCPProtocol) Discover(context.Context, mcpclient.Connection) ([]mcpclient.Item, error) {
	return []mcpclient.Item{}, nil
}
func (dashboardMCPProtocol) Invoke(context.Context, mcpclient.Connection, mcpclient.Item, json.RawMessage, func() error) (payload json.RawMessage, toolError bool, callErr error) {
	return nil, false, mcpclient.ErrDenied
}
func TestMCPDashboardOwnerAndAdminIsolation(t *testing.T) {
	db, e := sqlite.New(filepath.Join(t.TempDir(), "mcp.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.RunMigrations(context.Background()); e != nil {
		t.Fatal(e)
	}
	crypt, _ := config.NewEncryptor(make([]byte, 32))
	vault := credential.NewStore(credential.NewStorageAdapter(db), crypt, zap.NewNop())
	service, e := mcpclient.New(mcpclient.Options{Repository: db, Vault: vault, Protocol: dashboardMCPProtocol{}, LockPath: filepath.Join(t.TempDir(), "mcp.lock"), Enabled: true, AllowedOrigins: []string{"https://fixture.example"}})
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	server := &Server{mcp: service}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		if c.Get("X-User") != "" {
			c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: c.Get("X-User"), Role: domain.UserRole(c.Get("X-Role"))})
		}
		return c.Next()
	})
	server.registerMCP(app.Group("/api"))
	request := func(method, path, user, role, body string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, "/api/mcp"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-User", user)
		r.Header.Set("X-Role", role)
		resp, err := app.Test(r, 10000)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s => %d want %d: %s", method, path, resp.StatusCode, want, b)
		}
		return b
	}
	request("GET", "", "", "", "", 401)
	body := request("POST", "/connections", "owner", "regular", `{"label":"Owner private","endpoint":"https://fixture.example/mcp","data_domain":"personal"}`, 201)
	var c mcpclient.Connection
	_ = json.Unmarshal(body, &c)
	request("GET", "/connections/"+c.ID, "other", "admin", "", 404)
	own := request("GET", "", "owner", "regular", "", 200)
	if !strings.Contains(string(own), "Owner private") {
		t.Fatal(string(own))
	}
	other := request("GET", "", "other", "admin", "", 200)
	if strings.Contains(string(other), "Owner private") {
		t.Fatal("admin saw foreign data")
	}
	request("POST", "/policy", "owner", "regular", `{"enabled":false}`, 403)
	request("POST", "/policy", "other", "admin", `{"enabled":false}`, 200)
	request("POST", "/connections", "owner", "regular", `{"label":"bad","endpoint":"https://fixture.example/mcp","data_domain":"personal","owner_id":"other"}`, 400)
	request("POST", "/connections/"+c.ID+"/credentials", "owner", "regular", `{"revision":1,"mode":"bearer","token":"TOKEN-CANARY"}`, 204)
	b := request("GET", "/connections/"+c.ID, "owner", "regular", "", 200)
	if strings.Contains(string(b), "TOKEN-CANARY") {
		t.Fatal("secret exposed")
	}
}
