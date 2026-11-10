package dashboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

type topicLinks struct{}

func (*topicLinks) AddInstance(context.Context, domain.ChannelInstance) error    { return nil }
func (*topicLinks) UpdateInstance(context.Context, domain.ChannelInstance) error { return nil }
func (*topicLinks) StopInstance(string)                                          {}
func (*topicLinks) IsRunning(string) bool                                        { return true }
func (*topicLinks) TelegramTopicLink(_ context.Context, chat, owner string) (string, error) {
	if owner == "alice" && chat == "123" {
		return "https://t.me/BotAI/77", nil
	}
	return "", errors.New("unavailable")
}

func TestChatTopicLinkOnlyForAuthenticatedOwner(t *testing.T) {
	for _, owner := range []string{"alice", "foreign-admin", ""} {
		t.Run(owner, func(t *testing.T) {
			s := &Server{store: &mobileBoundaryRepo{}, channelManager: &topicLinks{}}
			app := fiber.New()
			if owner != "" {
				app.Use(func(c *fiber.Ctx) error {
					c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: owner, Role: domain.RoleAdmin})
					return c.Next()
				})
			}
			app.Get("/api/chats", s.handleChats)
			r, e := app.Test(httptest.NewRequest(http.MethodGet, "/api/chats", http.NoBody))
			if e != nil {
				t.Fatal(e)
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			has := strings.Contains(string(b), "https://t.me/BotAI/77")
			if has != (owner == "alice") {
				t.Fatalf("owner link isolation failed for %q: %s", owner, b)
			}
		})
	}
}
