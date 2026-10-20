package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/config"
	"github.com/iulita-ai/iulita/internal/credential"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/remoteaccess"
	"github.com/iulita-ai/iulita/internal/scheduler"
	"github.com/iulita-ai/iulita/internal/skill"
	remoteskill "github.com/iulita-ai/iulita/internal/skill/remoteaccess"
	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
)

type scheduledForbiddenTool struct {
	name  string
	calls int
}

func (s *scheduledForbiddenTool) Name() string { return s.name }
func (*scheduledForbiddenTool) Description() string {
	return "must not be exposed to generic schedules"
}
func (*scheduledForbiddenTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *scheduledForbiddenTool) Execute(context.Context, json.RawMessage) (string, error) {
	s.calls++
	return "unexpected execution", nil
}

type scheduledMaliciousProvider struct {
	t           *testing.T
	calls       int
	remoteInput json.RawMessage
}

func (p *scheduledMaliciousProvider) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	p.calls++
	for _, tool := range r.Tools {
		if tool.Name == "remote_runbook" || tool.Name == "delegate" || tool.Name == "orchestrate" {
			p.t.Fatalf("generic scheduler exposed recursive or SSH tool %s", tool.Name)
		}
	}
	if p.calls == 1 {
		input := p.remoteInput
		if input == nil {
			input = json.RawMessage(`{}`)
		}
		return llm.Response{ToolCalls: []llm.ToolCall{{ID: "ssh", Name: "remote_runbook", Input: input}, {ID: "delegate", Name: "delegate", Input: json.RawMessage(`{}`)}, {ID: "orchestrate", Name: "orchestrate", Input: json.RawMessage(`{}`)}}}, nil
	}
	return llm.Response{Content: "done"}, nil
}
func TestGenericScheduleCannotCallSSHOrDelegateEvenIfModelRequestsIt(t *testing.T) {
	registry := skill.NewRegistry()
	tools := []*scheduledForbiddenTool{{name: "remote_runbook"}, {name: "delegate"}, {name: "orchestrate"}}
	for _, tool := range tools {
		registry.Register(tool)
	}
	p := &scheduledMaliciousProvider{t: t}
	h := NewAgentJobHandler(memStore(t), p, registry, nil, nil, zap.NewNop())
	if _, e := handle(t, h, agentJobPayload{JobID: 1, UserID: "owner", Prompt: "execute SSH via any route"}); e != nil {
		t.Fatal(e)
	}
	for _, tool := range tools {
		if tool.calls != 0 {
			t.Fatalf("unattended recursive bypass executed %s", tool.name)
		}
	}
}

type scheduledSSHTransport struct{ calls int }

func (t *scheduledSSHTransport) Run(context.Context, remoteaccess.TransportRequest) (remoteaccess.TransportResult, error) {
	t.calls++
	code := 0
	return remoteaccess.TransportResult{Dispatched: true, ExitCode: &code}, nil
}
func TestTypedScheduleDispatchesRunbookWithoutLLMAndNeverReplays(t *testing.T) {
	ctx := context.Background()
	store := memStore(t)
	if e := store.CreateUser(ctx, &domain.User{ID: "owner", Username: "ssh-owner", Role: domain.RoleAdmin}); e != nil {
		t.Fatal(e)
	}
	crypt, e := config.NewEncryptor(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	vault := credential.NewStore(credential.NewStorageAdapter(store), crypt, zap.NewNop())
	transport := &scheduledSSHTransport{}
	service, e := remoteaccess.NewService(filepath.Join(t.TempDir(), "ssh.json"), vault, transport)
	if e != nil {
		t.Fatal(e)
	}
	defer service.Close()
	sc := remoteaccess.Scope{OwnerID: "owner", CallerID: "dashboard"}
	pub, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	hostKey, e := ssh.NewPublicKey(pub)
	if e != nil {
		t.Fatal(e)
	}
	server, e := service.PutServer(sc, remoteaccess.Server{Address: "127.0.0.1", Port: 22, HostKeys: []string{string(ssh.MarshalAuthorizedKey(hostKey))}, TrustSource: "test-console", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	identity, e := service.PutIdentity(ctx, sc, remoteaccess.Identity{Username: "ops", AuthType: "password", Enabled: true}, &remoteaccess.Secret{Password: "private-test-password"}) // gitleaks:allow -- Synthetic fixture credential, never used with real servers.
	if e != nil {
		t.Fatal(e)
	}
	route, e := service.PutRoute(sc, remoteaccess.Route{Hops: []remoteaccess.Hop{{ServerID: server.ID, IdentityID: identity.ID}}, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	book, e := service.PutRunbook(sc, remoteaccess.Runbook{RouteID: route.ID, Command: "true", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.PutGrant(sc, remoteaccess.Grant{RouteID: route.ID, CallerID: "scheduler", Capability: "runbook", RunbookID: book.ID, MaxUses: 5, Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}); e != nil {
		t.Fatal(e)
	}
	if e = service.SetEnabled(true); e != nil {
		t.Fatal(e)
	}
	// A valid standing grant and enabled real SSH skill must not make it
	// callable by an ordinary scheduled LLM, even through hallucinated calls.
	registry := skill.NewRegistry()
	registry.Register(remoteskill.New(service, "runbook"))
	registry.EnableSkill("remote_runbook")
	validInput, marshalErr := json.Marshal(remoteaccess.RunbookRequest{RunbookID: book.ID})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	malicious := &scheduledMaliciousProvider{t: t, remoteInput: validInput}
	generic := NewAgentJobHandler(store, malicious, registry, nil, nil, zap.NewNop())
	generic.SetRemoteAccess(service)
	if _, genericErr := handle(t, generic, agentJobPayload{JobID: 999, UserID: "owner", Prompt: "call the authorized SSH runbook"}); genericErr != nil {
		t.Fatal(genericErr)
	}
	if transport.calls != 0 {
		t.Fatal("generic scheduled hallucinated toolcall reached real SSH transport")
	}
	request, e := json.Marshal(remoteaccess.RunbookRequest{RunbookID: book.ID})
	if e != nil {
		t.Fatal(e)
	}
	job := &domain.AgentJob{UserID: "owner", Name: "SSH", Prompt: RemoteRunbookPromptPrefix + string(request), Enabled: true, Interval: "1h"}
	if e = store.CreateAgentJob(ctx, job); e != nil {
		t.Fatal(e)
	}
	p := &fixedProvider{content: "must never run"}
	h := NewAgentJobHandler(store, p, skill.NewRegistry(), nil, nil, zap.NewNop())
	h.SetRemoteAccess(service)
	raw, e := json.Marshal(agentJobPayload{JobID: job.ID, UserID: job.UserID, Prompt: job.Prompt})
	if e != nil {
		t.Fatal(e)
	}
	firing := scheduler.WithTaskID(ctx, 123)
	first, e := h.Handle(firing, string(raw))
	if e != nil {
		t.Fatal(e)
	}
	second, e := h.Handle(firing, string(raw))
	if e != nil {
		t.Fatal(e)
	}
	var x remoteaccess.Execution
	if e = json.Unmarshal([]byte(first), &x); e != nil {
		t.Fatal(e)
	}
	if x.State != "completed" || x.ID == "" || second != first || transport.calls != 1 || p.calls != 0 {
		t.Fatalf("typed scheduling/replay failed first=%s second=%s transport%d llm%d", first, second, transport.calls, p.calls)
	}
}
