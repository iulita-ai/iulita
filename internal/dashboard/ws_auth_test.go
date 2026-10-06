package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	clientws "github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/channel/webchat"
	"github.com/iulita-ai/iulita/internal/domain"
	"go.uber.org/zap"
)

func websocketToken(t *testing.T, expires *jwt.NumericDate) string {
	t.Helper()
	claims := auth.Claims{UserID: "actor", Username: "stale-name", Role: domain.RoleAdmin,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: expires}}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestWebSocketAuthorization(t *testing.T) {
	repo := &accessUserRepo{user: &domain.User{ID: "actor", Username: "current-name", Role: domain.RoleAdmin}}
	s := &Server{store: repo, authService: auth.NewService(repo, testJWTSecret, time.Hour, time.Hour)}
	app := fiber.New()
	app.Use("/ws", s.authenticateWebSocket)
	app.Get("/ws", func(c *fiber.Ctx) error { return c.JSON(auth.GetClaims(c)) })
	app.Get("/ws/chat", func(c *fiber.Ctx) error { return c.JSON(auth.GetClaims(c)) })
	valid := websocketToken(t, jwt.NewNumericDate(time.Now().Add(time.Hour)))
	for _, tc := range []struct {
		name, path, token, origin, header, forwardedProto string
		role                                              domain.UserRole
		password                                          bool
		want                                              int
	}{
		{name: "missing", path: "/ws/chat", role: domain.RoleAdmin, want: 401},
		{name: "invalid", path: "/ws/chat", token: "invalid", role: domain.RoleAdmin, want: 401},
		{name: "expired", path: "/ws/chat", token: websocketToken(t, jwt.NewNumericDate(time.Now().Add(-time.Minute))), role: domain.RoleAdmin, want: 401},
		{name: "unbounded", path: "/ws/chat", token: websocketToken(t, nil), role: domain.RoleAdmin, want: 401},
		{name: "chat", path: "/ws/chat", token: valid, role: domain.RoleRegular, want: 200},
		{name: "foreign actor", path: "/ws/chat?user_id=other", token: valid, role: domain.RoleAdmin, want: 403},
		{name: "foreign chat", path: "/ws/chat?chat_id=web:other", token: valid, role: domain.RoleAdmin, want: 403},
		{name: "own chat", path: "/ws/chat?user_id=actor&chat_id=web:actor&username=forged", token: valid, role: domain.RoleAdmin, want: 200},
		{name: "demoted admin", path: "/ws", token: valid, role: domain.RoleRegular, want: 403},
		{name: "admin events", path: "/ws", token: valid, role: domain.RoleAdmin, want: 200},
		{name: "initial password", path: "/ws/chat", token: valid, role: domain.RoleAdmin, password: true, want: 403},
		{name: "foreign origin", path: "/ws/chat", token: valid, origin: "https://attacker.example", role: domain.RoleAdmin, want: 403},
		{name: "same origin", path: "/ws/chat", token: valid, origin: "http://example.com", role: domain.RoleAdmin, want: 200},
		{name: "TLS proxy origin", path: "/ws/chat", token: valid, origin: "https://example.com", forwardedProto: "wss", role: domain.RoleAdmin, want: 200},
		{name: "plain proxy origin", path: "/ws/chat", token: valid, origin: "http://example.com", forwardedProto: "ws", role: domain.RoleAdmin, want: 200},
		{name: "TLS proxy downgrade", path: "/ws/chat", token: valid, origin: "http://example.com", forwardedProto: "wss", role: domain.RoleAdmin, want: 403},
		{name: "plain proxy upgrade", path: "/ws/chat", token: valid, origin: "https://example.com", forwardedProto: "ws", role: domain.RoleAdmin, want: 403},
		{name: "TLS proxy foreign origin", path: "/ws/chat", token: valid, origin: "https://attacker.example", forwardedProto: "wss", role: domain.RoleAdmin, want: 403},
		{name: "invalid origin", path: "/ws/chat", token: valid, origin: "null", role: domain.RoleAdmin, want: 403},
		{name: "malformed bearer", path: "/ws/chat", token: valid, header: "Basic invalid", role: domain.RoleAdmin, want: 401},
		{name: "bearer", path: "/ws/chat", header: "Bearer " + valid, role: domain.RoleAdmin, want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo.user.Role, repo.user.MustChangePass = tc.role, tc.password
			u, err := url.Parse("http://example.com" + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			if tc.token != "" {
				q.Set("token", tc.token)
			}
			u.RawQuery = q.Encode()
			req := httptest.NewRequest(http.MethodGet, u.String(), http.NoBody)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Authorization", tc.header)
			if tc.forwardedProto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwardedProto)
			}
			res, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", res.StatusCode, tc.want)
			}
			if tc.want == 200 {
				var claims auth.Claims
				if err := json.NewDecoder(res.Body).Decode(&claims); err != nil {
					t.Fatal(err)
				}
				if claims.UserID != "actor" || claims.Username != "current-name" || claims.Role != tc.role {
					t.Fatal("identity did not come from the current database user")
				}
			}
		})
	}
	repo.user = nil
	req := httptest.NewRequest(http.MethodGet, "/ws/chat?"+url.Values{"token": {valid}}.Encode(), http.NoBody)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("deleted user admitted")
	}
}

func TestProductionWebSocketRoutesRequireAuthAndBindChatIdentity(t *testing.T) {
	repo := &accessUserRepo{user: &domain.User{ID: "actor", Username: "current-name", Role: domain.RoleAdmin}}
	wc := webchat.New(zap.NewNop())
	received := make(chan channel.IncomingMessage, 8)
	workCancelled := make(chan struct{}, 2)
	workStarted := make(chan struct{}, 2)
	startCtx, cancelStart := context.WithCancel(context.Background())
	cancelStart()
	if startErr := wc.Start(startCtx, func(ctx context.Context, msg channel.IncomingMessage) (string, error) {
		received <- msg
		if msg.Text == "wait" {
			workStarted <- struct{}{}
			<-ctx.Done()
			workCancelled <- struct{}{}
			return "", ctx.Err()
		}
		return "release-test-ok", nil
	}); startErr != nil && !errors.Is(startErr, context.Canceled) {
		t.Fatal(startErr)
	}
	hub := NewWSHub(zap.NewNop())
	s := New(Config{Store: repo, AuthService: auth.NewService(repo, testJWTSecret, time.Hour, time.Hour),
		WSHub: hub, WebChat: wc, Logger: zap.NewNop(), StaticFS: minimalStaticFS()})
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	go func() { _ = s.app.Listener(listener) }()
	t.Cleanup(func() { _ = s.app.Shutdown() })
	base := "ws://" + listener.Addr().String()
	for _, path := range []string{"/ws", "/ws/chat?user_id=actor"} {
		conn, res, err := clientws.DefaultDialer.Dial(base+path, nil)
		if conn != nil {
			_ = conn.Close()
		}
		if res == nil || res.StatusCode != 401 || err == nil {
			t.Fatal("unauthenticated production WebSocket route admitted")
		}
		_ = res.Body.Close()
	}
	token := websocketToken(t, jwt.NewNumericDate(time.Now().Add(2*time.Second)))
	conn, _, err := clientws.DefaultDialer.Dial(base+"/ws/chat?username=forged", http.Header{"Authorization": {"Bearer " + token}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]string{"text": "synthetic release check", "chat_id": "web:other"}); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-received:
		if msg.UserID != "actor" || msg.ResolvedUserID != "actor" || msg.ChatID != "web:actor" || msg.UserName != "current-name" {
			t.Fatal("chat message identity controlled by the client")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authenticated chat did not deliver")
	}
	if err := conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]string{"text": "wait"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("waiting work did not start")
	}
	select {
	case <-workCancelled:
	case <-time.After(4 * time.Second):
		t.Fatal("JWT expiry did not cancel in-flight work")
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expired WebSocket remained open")
	} else {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("client timeout instead of server closing expired connection")
		}
	}
	replacementToken := websocketToken(t, jwt.NewNumericDate(time.Now().Add(time.Minute)))
	headers := http.Header{"Authorization": {"Bearer " + replacementToken}}
	oldConn, _, dialErr := clientws.DefaultDialer.Dial(base+"/ws/chat", headers)
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	defer oldConn.Close()
	if writeErr := oldConn.WriteJSON(map[string]string{"text": "wait"}); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement work did not start")
	}
	newConn, _, replacementErr := clientws.DefaultDialer.Dial(base+"/ws/chat", headers)
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	defer newConn.Close()
	select {
	case <-workCancelled:
	case <-time.After(time.Second):
		t.Fatal("replaced connection did not cancel pending work")
	}
	if writeErr := newConn.WriteJSON(map[string]string{"text": "replacement check"}); writeErr != nil {
		t.Fatal(writeErr)
	}
	if deadlineErr := newConn.SetReadDeadline(time.Now().Add(2 * time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	var reply map[string]any
	if readErr := newConn.ReadJSON(&reply); readErr != nil {
		t.Fatal(readErr)
	}
	if reply["text"] != "release-test-ok" {
		t.Fatal("old teardown removed replacement connection")
	}

	events, _, eventsErr := clientws.DefaultDialer.Dial(base+"/ws", headers)
	if eventsErr != nil {
		t.Fatal(eventsErr)
	}
	defer events.Close()
	if deadlineErr := events.SetReadDeadline(time.Now().Add(3 * time.Second)); deadlineErr != nil {
		t.Fatal(deadlineErr)
	}
	if readErr := events.ReadJSON(&reply); readErr != nil {
		t.Fatal(readErr)
	}
	if reply["type"] != "connected" {
		t.Fatal("admin event connection unavailable")
	}
	var writers sync.WaitGroup
	for i := range 20 {
		writers.Add(1)
		go func() { defer writers.Done(); hub.Broadcast(WSMessage{Type: "synthetic", Payload: i}) }()
	}
	for range 20 {
		if readErr := events.ReadJSON(&reply); readErr != nil {
			t.Fatal(readErr)
		}
		if reply["type"] != "synthetic" {
			t.Fatal("unexpected event")
		}
	}
	writers.Wait()

}
