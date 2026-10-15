package dashboard

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/storage"
)

type mobileBoundaryRepo struct{ storage.Repository }

func (*mobileBoundaryRepo) GetChatIDs(context.Context) ([]string, error) {
	return []string{"mobile:conversation:private", "MOBILE:invalid", "web:other", "123"}, nil
}
func (*mobileBoundaryRepo) CountMessages(_ context.Context, id string) (int, error) {
	if domain.IsMobileChatID(id) {
		panic("private ID reached repository")
	}
	return 1, nil
}

// Other methods deliberately panic via the embedded nil interface if reached.
func TestLegacyRoutesDenyMobileRegardlessOfActor(t *testing.T) {
	for _, actor := range []string{"foreign-user", "foreign-admin", "auth-disabled"} {
		t.Run(actor, func(t *testing.T) {
			s := &Server{store: &mobileBoundaryRepo{}}
			app := fiber.New()
			if actor != "auth-disabled" {
				app.Use(func(c *fiber.Ctx) error {
					role := domain.RoleRegular
					if actor == "foreign-admin" {
						role = domain.RoleAdmin
					}
					c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: "other", Role: role})
					return c.Next()
				})
			}
			app.Get("/api/messages", s.handleMessages)
			app.Get("/api/directives", s.handleDirectives)
			app.Get("/api/chats", s.handleChats)
			for _, path := range []string{"/api/messages?chat_id=mobile%3Aconversation%3Aprivate", "/api/messages?chat_id=MOBILE%3Ainvalid&before_id=50", "/api/directives?chat_id=mobile%3Aconversation%3Aprivate"} {
				res, err := app.Test(httptest.NewRequest("GET", path, http.NoBody))
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode != 404 || strings.Contains(string(b), "private") {
					t.Fatalf("%s: %d %s", path, res.StatusCode, b)
				}
			}
			res, err := app.Test(httptest.NewRequest("GET", "/api/chats", http.NoBody))
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 || strings.Contains(strings.ToLower(string(b)), "mobile:") || !strings.Contains(string(b), "web:other") {
				t.Fatalf("list: %d %s", res.StatusCode, b)
			}
		})
	}
}
