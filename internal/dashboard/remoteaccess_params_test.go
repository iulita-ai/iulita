package dashboard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"github.com/valyala/fasthttp"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

func TestRemotePutOwnsCatalogIDsAfterRequestBufferReuse(t *testing.T) {
	for _, kind := range []string{"servers", "identities", "routes", "grants", "runbooks"} {
		t.Run(kind, func(t *testing.T) {
			catalogPath := filepath.Join(t.TempDir(), "catalog.json")
			store, err := sqlite.New(filepath.Join(t.TempDir(), "app.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = store.RunMigrations(context.Background()); err != nil {
				t.Fatal(err)
			}
			enc, err := config.NewEncryptor(make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			vault := credential.NewStore(credential.NewStorageAdapter(store), enc, zap.NewNop())
			svc, err := remoteaccess.NewService(catalogPath, vault, &dashboardSSHTransport{})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			sc := remoteaccess.Scope{OwnerID: "owner", CallerID: "dashboard"}
			pub, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			key, err := ssh.NewPublicKey(pub)
			if err != nil {
				t.Fatal(err)
			}
			servers := make([]remoteaccess.Server, 0, 2)
			for range 2 {
				server, putErr := svc.PutServer(sc, remoteaccess.Server{Address: "127.0.0.1", Port: 22, HostKeys: []string{string(ssh.MarshalAuthorizedKey(key))}, TrustSource: "test console", Enabled: true})
				if putErr != nil {
					t.Fatal(putErr)
				}
				servers = append(servers, server)
			}
			type catalogRecord struct {
				id   string
				body any
			}
			records := make([]catalogRecord, 0, len(servers))
			for _, server := range servers {
				var identity remoteaccess.Identity
				identity, err = svc.PutIdentity(context.Background(), sc, remoteaccess.Identity{Username: "deploy", AuthType: "password", Enabled: true}, &remoteaccess.Secret{Password: "TEST_ONLY"})
				if err != nil {
					t.Fatal(err)
				}
				var route remoteaccess.Route
				route, err = svc.PutRoute(sc, remoteaccess.Route{Hops: []remoteaccess.Hop{{ServerID: server.ID, IdentityID: identity.ID}}, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				var grant remoteaccess.Grant
				grant, err = svc.PutGrant(sc, remoteaccess.Grant{RouteID: route.ID, CallerID: "dashboard", Capability: "exec", ExpiresAt: time.Now().Add(time.Hour), Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				var book remoteaccess.Runbook
				book, err = svc.PutRunbook(sc, remoteaccess.Runbook{RouteID: route.ID, Command: "uptime", Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				var id string
				var body any
				switch kind {
				case "servers":
					id, body = server.ID, server
				case "identities":
					id, body = identity.ID, struct {
						Identity remoteaccess.Identity `json:"identity"`
					}{identity}
				case "routes":
					id, body = route.ID, route
				case "grants":
					id, body = grant.ID, grant
				case "runbooks":
					id, body = book.ID, book
				}
				records = append(records, catalogRecord{id, body})
			}
			s := &Server{remoteAccess: svc}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error {
				c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: "owner", Role: domain.RoleAdmin})
				return c.Next()
			})
			if kind == "runbooks" {
				app.Put("/"+kind+"/:id", s.remotePutRunbook)
			} else {
				app.Put("/"+kind+"/:id", func(c *fiber.Ctx) error { return s.remotePut(c, kind) })
			}
			handler := app.Handler()
			var requestCtx fasthttp.RequestCtx
			for i, record := range records {
				body, marshalErr := json.Marshal(record.body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				requestCtx.Request.Header.SetMethod("PUT")
				requestCtx.Request.Header.SetContentType("application/json")
				requestCtx.Request.SetRequestURI("/" + kind + "/" + record.id)
				requestCtx.Request.SetBody(body)
				handler(&requestCtx)
				if requestCtx.Response.StatusCode() != fiber.StatusOK {
					t.Fatalf("update %d: status=%d body=%s", i, requestCtx.Response.StatusCode(), requestCtx.Response.Body())
				}
				// Fiber Params borrows the URI path. Deterministically simulate fasthttp
				// reusing that backing buffer after the handler has returned.
				path := requestCtx.Request.URI().Path()
				for j := len(path) - len(record.id); j < len(path); j++ {
					path[j] = 'x'
				}
			}
			// Flush after buffer reuse, as a subsequent catalog write would do.
			if err = svc.SetEnabled(false); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(catalogPath)
			if err != nil {
				t.Fatal(err)
			}
			var persisted remoteaccess.Catalog
			if err := json.Unmarshal(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			for _, record := range records {
				var id string
				var ok bool
				switch kind {
				case "servers":
					x, exists := persisted.Servers[record.id]
					id, ok = x.ID, exists
				case "identities":
					x, exists := persisted.Identities[record.id]
					id, ok = x.ID, exists
				case "routes":
					x, exists := persisted.Routes[record.id]
					id, ok = x.ID, exists
				case "grants":
					x, exists := persisted.Grants[record.id]
					id, ok = x.ID, exists
				case "runbooks":
					x, exists := persisted.Runbooks[record.id]
					id, ok = x.ID, exists
				}
				if !ok || id != record.id {
					t.Errorf("persisted %s %q: exists=%v record ID=%q after request buffer reuse", kind, record.id, ok, id)
				}
			}
		})
	}
}
