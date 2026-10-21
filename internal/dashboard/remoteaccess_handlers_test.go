package dashboard

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	gorillaws "github.com/gorilla/websocket"
	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/storage/sqlite"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type dashboardSSHTransport struct {
	calls atomic.Int32
	opens atomic.Int32
}

func (t *dashboardSSHTransport) OpenPTY(ctx context.Context, _ remoteaccess.PTYRequest) (remoteaccess.PTYSession, error) {
	t.opens.Add(1)
	r, w := io.Pipe()
	p := &dashboardPTYSession{r: r, w: w, done: make(chan struct{})}
	go func() { <-ctx.Done(); p.Close() }()
	return p, nil
}

type dashboardPTYSession struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	once sync.Once
	done chan struct{}
}

func (p *dashboardPTYSession) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *dashboardPTYSession) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *dashboardPTYSession) Resize(int, int) error       { return nil }
func (p *dashboardPTYSession) Wait() error {
	<-p.done
	return errors.New("transport cancelled; remote unknown")
}
func (p *dashboardPTYSession) Close() error {
	p.once.Do(func() { p.r.Close(); p.w.Close(); close(p.done) })
	return nil
}

func (t *dashboardSSHTransport) Run(context.Context, remoteaccess.TransportRequest) (remoteaccess.TransportResult, error) {
	t.calls.Add(1)
	code := 0
	return remoteaccess.TransportResult{ExitCode: &code, Dispatched: true, Stdout: []byte("PRIVATE_OUTPUT_CANARY")}, nil
}
func TestRemoteAccessDashboardConsentAndIsolation(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.New(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.RunMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	enc, _ := config.NewEncryptor(make([]byte, 32))
	vault := credential.NewStore(credential.NewStorageAdapter(store), enc, zap.NewNop())
	tr := &dashboardSSHTransport{}
	svc, err := remoteaccess.NewService(filepath.Join(t.TempDir(), "ssh", "catalog.json"), vault, tr)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err = store.CreateUser(ctx, &domain.User{ID: "owner", Username: "owner", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateUser(ctx, &domain.User{ID: "foreign", Username: "foreign", Role: domain.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, terminalTickets: newTerminalTicketStore(), remoteAccess: svc, remoteConfirms: newRemoteConfirmations(), registry: skill.NewRegistry()}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		if c.Get("X-Test-User") != "" {
			c.Locals(auth.ContextKeyUser, &auth.Claims{UserID: c.Get("X-Test-User"), Role: domain.UserRole(c.Get("X-Test-Role")), RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
		}
		return c.Next()
	})
	app.Get("/api/remote-access/terminals/connect/:ticket", s.remoteTerminalUpgrade)
	s.registerRemoteAccess(app.Group("/api"))
	request := func(method, path, user, role string, body any, want int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "/api/remote-access"+path, strings.NewReader(string(data)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Test-User", user)
		r.Header.Set("X-Test-Role", role)
		resp, e := app.Test(r, 10000)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s got%d want%d %s", method, path, resp.StatusCode, want, b)
		}
		return b
	}
	admin := string(domain.RoleAdmin)
	request("GET", "", "", "", nil, 401)
	request("POST", "/enabled", "owner", string(domain.RoleRegular), map[string]bool{"enabled": true}, 403)
	request("POST", "/servers", "owner", string(domain.RoleRegular), map[string]any{}, 403)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pk, _ := ssh.NewPublicKey(pub)
	var server remoteaccess.Server
	json.Unmarshal(request("POST", "/servers", "owner", admin, remoteaccess.Server{Address: "127.0.0.1", Port: 22, HostKeys: []string{string(ssh.MarshalAuthorizedKey(pk))}, TrustSource: "console", Enabled: true}, 200), &server)
	var identity remoteaccess.Identity
	json.Unmarshal(request("POST", "/identities", "owner", admin, map[string]any{"identity": remoteaccess.Identity{Username: "deploy", AuthType: "password", Enabled: true}, "secret": remoteaccess.Secret{Password: "PASSWORD_CANARY"}}, 200), &identity)
	var route remoteaccess.Route
	json.Unmarshal(request("POST", "/routes", "owner", admin, remoteaccess.Route{Hops: []remoteaccess.Hop{{ServerID: server.ID, IdentityID: identity.ID}}, Enabled: true}, 200), &route)
	var grant remoteaccess.Grant
	if err := json.Unmarshal(request("POST", "/grants", "owner", admin, remoteaccess.Grant{RouteID: route.ID, CallerID: "dashboard", Capability: "exec", ExpiresAt: time.Now().Add(time.Hour), Enabled: true}, 200), &grant); err != nil {
		t.Fatal(err)
	}
	grant.Permanent = true
	request("PUT", "/grants/"+grant.ID, "owner", admin, grant, 403) // Contradictory date and flag.
	grant.ExpiresAt = time.Time{}
	request("PUT", "/grants/"+grant.ID, "owner", string(domain.RoleRegular), grant, 403)
	request("PUT", "/grants/"+grant.ID, "foreign", admin, grant, 403)
	if err := json.Unmarshal(request("PUT", "/grants/"+grant.ID, "owner", admin, grant, 200), &grant); err != nil {
		t.Fatal(err)
	}
	if !grant.Permanent || !grant.ExpiresAt.IsZero() {
		t.Fatal("permanent lifetime did not survive HTTP round trip")
	}
	request("POST", "/enabled", "owner", admin, map[string]bool{"enabled": true}, 200)
	req := remoteaccess.ExecuteRequest{RouteID: route.ID, Command: "uptime", TimeoutSeconds: 60}
	request("POST", "/executions", "owner", admin, req, 400)
	var preview remoteaccess.Approval
	json.Unmarshal(request("POST", "/executions/preview", "owner", admin, req, 200), &preview)
	req.ExpectedPlanHash = preview.PlanHash
	req.ApprovalID = preview.ApprovalID
	var run remoteaccess.Execution
	body := request("POST", "/executions", "owner", admin, req, 200)
	json.Unmarshal(body, &run)
	if strings.Contains(string(body), "CANARY") || run.State != "completed" || tr.calls.Load() != 1 {
		t.Fatalf("leak or failed execution %s", body)
	}
	request("POST", "/executions", "owner", admin, req, 200)
	if tr.calls.Load() != 1 {
		t.Fatal("replayed mutation")
	}
	foreign := request("GET", "", "foreign", admin, nil, 200)
	if strings.Contains(string(foreign), server.ID) || strings.Contains(string(foreign), identity.ID) {
		t.Fatal("cross-owner metadata leak")
	}
	request("GET", "/artifacts/"+run.ArtifactID, "foreign", admin, nil, 404)
	artifact := request("GET", "/artifacts/"+run.ArtifactID, "owner", admin, nil, 200)
	if !strings.Contains(string(artifact), "PRIVATE_OUTPUT_CANARY") {
		t.Fatal("private artifact missing")
	}
	snapshot := request("GET", "", "owner", admin, nil, 200)
	if strings.Contains(string(snapshot), "CANARY") {
		t.Fatal("snapshot leak")
	}
	// Typed operations require their own root-scoped grant and exact approval.
	fileReq := remoteaccess.ExecuteRequest{RouteID: route.ID, Operation: "file_stat", File: &remoteaccess.FileRequest{Root: "/srv/private", Path: "result.txt"}, TimeoutSeconds: 60}
	request("POST", "/operations/preview", "owner", admin, fileReq, 403)
	request("POST", "/grants", "owner", admin, remoteaccess.Grant{RouteID: route.ID, CallerID: "dashboard", Capability: "file_stat", FileRoot: "/srv/private", ExpiresAt: time.Now().Add(time.Hour), Enabled: true}, 200)
	var fileApproval remoteaccess.Approval
	json.Unmarshal(request("POST", "/operations/preview", "owner", admin, fileReq, 200), &fileApproval)
	fileReq.ExpectedPlanHash = fileApproval.PlanHash
	fileReq.ApprovalID = fileApproval.ApprovalID
	request("POST", "/executions", "owner", admin, fileReq, 403)
	request("POST", "/operations", "owner", admin, fileReq, 200)
	// Creating a runbook or schedule is admin-only and owner bound.
	bookBody := remoteaccess.Runbook{Label: "health", RouteID: route.ID, Command: "uptime", Enabled: true}
	request("POST", "/runbooks", "owner", string(domain.RoleRegular), bookBody, 403)
	var book remoteaccess.Runbook
	json.Unmarshal(request("POST", "/runbooks", "owner", admin, bookBody, 200), &book)
	request("POST", "/grants", "owner", admin, remoteaccess.Grant{RouteID: route.ID, CallerID: "main", Capability: "runbook", RunbookID: book.ID, MaxUses: 1, ExpiresAt: time.Now().Add(time.Hour), Enabled: true}, 200)
	request("POST", "/runbooks/execute", "owner", admin, remoteaccess.RunbookRequest{RunbookID: book.ID, Parameters: map[string]string{}}, 200)
	request("POST", "/runbooks/execute", "owner", admin, remoteaccess.RunbookRequest{RunbookID: book.ID, Parameters: map[string]string{}}, 403)
	request("POST", "/grants", "owner", admin, remoteaccess.Grant{RouteID: route.ID, CallerID: "scheduler", Capability: "runbook", RunbookID: book.ID, MaxUses: 2, ExpiresAt: time.Now().Add(time.Hour), Enabled: true}, 200)
	schedule := request("POST", "/schedules", "owner", admin, map[string]any{"name": "health", "interval": "1h", "request": remoteaccess.RunbookRequest{RunbookID: book.ID, Parameters: map[string]string{}}}, 201)
	var job domain.AgentJob
	json.Unmarshal(schedule, &job)
	if job.UserID != "owner" || !strings.HasPrefix(job.Prompt, "iulita-ssh-runbook-v1:") {
		t.Fatal("schedule is not typed owner scoped")
	}
	// A browser ticket is not reusable, cannot cross Origin, and cannot survive
	// user disable/security epoch changes after issuance.
	makeTicket := func() string {
		t.Helper()
		body := request("POST", "/terminals/ticket", "owner", admin, remoteaccess.TerminalRequest{RouteID: route.ID, ApprovalID: "candidate", ExpectedPlanHash: "candidate", Options: remoteaccess.TerminalOptions{Rows: 24, Cols: 80}}, 200)
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		json.Unmarshal(body, &ticket)
		return ticket.Ticket
	}
	checkTicket := func(id, origin string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://example.com/api/remote-access/terminals/connect/"+id, http.NoBody)
		r.Header.Set("Origin", origin)
		response, e := app.Test(r)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("ticket origin=%s got%d want%d", origin, response.StatusCode, want)
		}
	}
	ticket := makeTicket()
	checkTicket(ticket, "http://foreign.test", 403)
	owner, e := store.GetUser(ctx, "owner")
	if e != nil {
		t.Fatal(e)
	}
	owner.Disabled = true
	if e = store.UpdateUser(ctx, owner); e != nil {
		t.Fatal(e)
	}
	checkTicket(ticket, "http://example.com", 401)
	checkTicket(ticket, "http://example.com", 401)
	owner.Disabled = false
	if e = store.UpdateUser(ctx, owner); e != nil {
		t.Fatal(e)
	}
	ticket = makeTicket()
	owner.SecurityEpoch++
	if e = store.UpdateUser(ctx, owner); e != nil {
		t.Fatal(e)
	}
	checkTicket(ticket, "http://example.com", 401)
	// Actual browser websocket relay: one ticket, one writer, echo and kill switch.
	request("POST", "/grants", "owner", admin, remoteaccess.Grant{RouteID: route.ID, CallerID: "dashboard", Capability: "terminal", ExpiresAt: time.Now().Add(time.Hour), Enabled: true}, 200)
	terminalReq := remoteaccess.TerminalRequest{RouteID: route.ID, Options: remoteaccess.TerminalOptions{Rows: 24, Cols: 80, Term: "xterm-256color", TimeoutSeconds: 60}}
	var terminalPreview remoteaccess.Approval
	json.Unmarshal(request("POST", "/terminals/preview", "owner", admin, terminalReq, 200), &terminalPreview)
	terminalReq.ApprovalID = terminalPreview.ApprovalID
	terminalReq.ExpectedPlanHash = terminalPreview.PlanHash
	var liveTicket struct {
		Ticket string `json:"ticket"`
	}
	json.Unmarshal(request("POST", "/terminals/ticket", "owner", admin, terminalReq, 200), &liveTicket)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); app.Listener(listener) }()
	defer func() { app.Shutdown(); <-serverDone }()
	socketURL := "ws://" + listener.Addr().String() + "/api/remote-access/terminals/connect/" + liveTicket.Ticket
	header := make(map[string][]string)
	header["Origin"] = []string{"http://" + listener.Addr().String()}
	ws, response, e := gorillaws.DefaultDialer.Dial(socketURL, header)
	if e != nil {
		t.Fatalf("websocket relay: %v", e)
	}
	if response != nil {
		response.Body.Close()
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame browserTerminalFrame
	if e = ws.ReadJSON(&frame); e != nil || frame.Kind != "ready" || frame.ExecutionID == "" {
		t.Fatalf("terminal ready %+v %v", frame, e)
	}
	liveExecutionID := frame.ExecutionID
	if e = ws.WriteJSON(browserTerminalFrame{Kind: "input", Data: []byte("PTY_PRIVATE_CANARY")}); e != nil {
		t.Fatal(e)
	}
	if e = ws.ReadJSON(&frame); e != nil || frame.Kind != "output" || string(frame.Data) != "PTY_PRIVATE_CANARY" {
		t.Fatalf("terminal echo %+v %v", frame, e)
	}
	_, replayResponse, replayErr := gorillaws.DefaultDialer.Dial(socketURL, header)
	if replayResponse != nil {
		defer replayResponse.Body.Close()
	}
	if replayErr == nil || tr.opens.Load() != 1 {
		t.Fatal("terminal ticket replayed")
	}
	if e = svc.SetEnabled(false); e != nil {
		t.Fatal(e)
	}
	ws.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, e = ws.ReadMessage(); e == nil {
		t.Fatal("disabled terminal stayed connected")
	}
	for _, execution := range svc.Snapshot(remoteaccess.Scope{OwnerID: "owner", CallerID: "dashboard"}).Executions {
		if execution.ID == liveExecutionID && execution.State == "completed" {
			t.Fatal("cancel claimed confirmed remote success")
		}
	}
}
