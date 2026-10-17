package transport

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	v1 "github.com/iulita-ai/iulita/api/gen/go/iulita/mobile/v1"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/httpartifact"
	"github.com/iulita-ai/iulita/internal/httpfetch"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/mobile/assistanttools"
	"github.com/iulita-ai/iulita/internal/mobile/browseroauth"
	"github.com/iulita-ai/iulita/internal/mobile/devices"
	"github.com/iulita-ai/iulita/internal/mobile/engine"
	"github.com/iulita-ai/iulita/internal/mobile/sqlitedb"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/askuser"
	"github.com/iulita-ai/iulita/internal/skill/craft"
	httpskill "github.com/iulita-ai/iulita/internal/skill/httpfetch"
	"github.com/iulita-ai/iulita/internal/skill/reminders"
	slackskill "github.com/iulita-ai/iulita/internal/skill/slack"
	"github.com/iulita-ai/iulita/internal/skill/slackpost"
	"github.com/iulita-ai/iulita/internal/skill/todoist"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type parityModel map[string]llm.ToolCall
type parityProvider struct {
	calls parityModel
	t     *testing.T
}

func (p parityProvider) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	if len(r.ToolExchanges) == 0 {
		if r.Message == "reminder" {
			input, _ := json.Marshal(map[string]string{"action": "create", "title": "REMINDER_CANARY", "due_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
			return llm.Response{ToolCalls: []llm.ToolCall{{ID: "rm", Name: "reminders", Input: input}}}, nil
		}
		call, ok := p.calls[r.Message]
		if ok {
			return llm.Response{ToolCalls: []llm.ToolCall{call}}, nil
		}
	}
	if len(r.ToolExchanges) > 0 {
		result := r.ToolExchanges[len(r.ToolExchanges)-1].Results[0]
		if result.IsError || strings.Contains(result.Content, "lookup_failed") || strings.Contains(result.Content, `"status":"rejected"`) {
			p.t.Errorf("%s did not execute: %s", r.Message, result.Content)
		}
		expected := map[string]string{"craft read": "CRAFT_CANARY", "craft write": "doc-created", "todoist read": "TODOIST_CANARY", "todoist write": "task-created", "slack read": "SLACK_CANARY", "slack write": "123.4", "choice": `"answer":"1"`, "text": "Helsinki", "http get": `"artifact"`, "http post": `"outcome":"completed"`, "reminder": "scheduled"}
		if want := expected[r.Message]; want != "" && !strings.Contains(result.Content, want) {
			p.t.Errorf("%s missing %s: %s", r.Message, want, result.Content)
		}
	}
	return llm.Response{Content: "simulator: operation observed"}, nil
}

type parityRoundTrip func(*http.Request) (*http.Response, error)

func (f parityRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type parityResolver struct{}

func (parityResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

type parityPoster struct{ posts atomic.Int32 }

func (*parityPoster) NativePostBinding(context.Context, string) string { return "fixture-bot-binding" }
func (p *parityPoster) PostBoundToChannel(ctx context.Context, channel, text, binding string) (string, error) {
	if binding != "fixture-bot-binding" {
		return "", io.ErrUnexpectedEOF
	}
	return p.PostToChannel(ctx, channel, text)
}
func (*parityPoster) WriteMode(context.Context, string) string { return "auto" }
func (p *parityPoster) PostToChannel(_ context.Context, channel, text string) (string, error) {
	if channel != "C_FIXTURE" || text != "approved fixture" {
		return "", io.ErrUnexpectedEOF
	}
	p.posts.Add(1)
	return "123.4", nil
}

// Real browser OAuth, TLS gRPC, persistent SQLite and the production catalog;
// only external service HTTP transports and the model are simulated.
func TestParitySimulatorsThroughOAuthGRPC(t *testing.T) {
	h := setupHarnessWithOptions(t, devices.Options{}, "assistant:services:read", "assistant:services:write")
	e := h.server.config.Engine
	<-e.Ready()
	owner := h.owners["owner"]
	var reads, writes atomic.Int32
	api := &http.Client{Transport: parityRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		if r.Method == http.MethodGet {
			reads.Add(1)
		} else {
			writes.Add(1)
		}
		body := `{"items":[]}`
		switch r.URL.Host {
		case "craft.fixture.test":
			if r.Header.Get("Authorization") != "Bearer craft-fixture" {
				t.Error("Craft owner credential substituted")
			}
			if r.Method == http.MethodPost {
				body = `{"items":[{"id":"doc-created"}]}`
			} else {
				body = `{"items":[{"documentId":"doc","markdown":"CRAFT_CANARY"}]}`
			}
		case "api.todoist.com":
			if r.Header.Get("Authorization") != "Bearer todoist-fixture" {
				t.Error("Todoist owner credential substituted")
			}
			if r.Method == http.MethodPost {
				body = `{"id":"task-created","content":"fixture task"}`
			} else {
				body = `{"results":[{"id":"p","name":"TODOIST_CANARY"}],"next_cursor":null}`
			}
		case "slack.com":
			body = `{"ok":true,"messages":{"matches":[{"text":"SLACK_CANARY","ts":"123","channel":{"id":"C_FIXTURE","name":"fixture"}}],"pagination":{"total_count":1}}}`
		default:
			t.Error("unexpected external host", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	r := skill.NewRegistry()
	r.SetCapabilities([]string{"craft", "todoist", "slack", "slack_search", "slack_write", "slack_oauth", "http", "proxy", "slack_user", "web"})
	cc := craft.NewClient("https://craft.fixture.test", "craft-fixture", api)
	cc.SetNativeOwner(owner)
	r.Register(craft.NewSearch(cc))
	r.Register(craft.NewRead(cc))
	r.Register(craft.NewWrite(cc))
	r.Register(craft.NewTasks(cc))
	tc := todoist.NewClient("todoist-fixture", api, zap.NewNop())
	tc.SetNativeOwner(owner)
	r.Register(todoist.NewSkill(tc, zap.NewNop()))
	account := &domain.SlackAccount{UserID: owner, SlackUserID: "U_FIXTURE", TeamID: "T_FIXTURE", Scopes: `["search:read"]`, EncryptedAccessToken: "fixture"}
	if err := h.db.SaveSlackAccount(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	sc := slackskill.NewClient(slackskill.ClientOptions{Store: h.db, HTTPClient: api, Logger: zap.NewNop()})
	r.Register(slackskill.NewSearchSkill(sc, h.db, zap.NewNop()))
	poster := &parityPoster{}
	sp := slackpost.NewPostSkill(h.db, zap.NewNop())
	sp.SetNativeOwner(owner)
	sp.SetChannelPoster(poster)
	r.Register(sp)
	r.Register(reminders.New(h.db))
	r.Register(askuser.New())
	artifacts, err := httpartifact.NewStore(t.TempDir(), h.db, httpartifact.Limits{TTL: time.Hour, QuotaGlobal: 1 << 20, QuotaUser: 1 << 20}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	artifacts.SetNativeGuard(func(ctx context.Context, s httpfetch.Scope) error {
		c, lookupErr := h.chats.Get(ctx, s.UserID, s.NativeConversation)
		if lookupErr != nil {
			return lookupErr
		}
		if c.Generation != s.NativeGeneration {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	h.server.SetHTTPArtifacts(artifacts)
	var httpReads, httpWrites atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			httpWrites.Add(1)
		} else {
			httpReads.Add(1)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "HTTP_ARTIFACT_CANARY")
	}))
	defer proxy.Close()
	limits := httpfetch.DefaultLimits()
	limits.MaxDownloadBytes = 1000
	limits.ConsentThresholdBytes = 10
	strict, perr := httpfetch.NewEngine(httpfetch.EngineConfig{ProxyURL: proxy.URL, Policy: httpfetch.Policy{Enabled: true}, Limits: limits, Resolver: parityResolver{}, Artifacts: artifacts})
	if perr != nil {
		t.Fatal(perr)
	}
	r.Register(httpskill.New(httpfetch.NewFactory(strict)))
	model := parityModel{
		"craft read":    {ID: "cr", Name: "craft_search", Input: json.RawMessage(`{"query":"fixture"}`)},
		"craft write":   {ID: "cw", Name: "craft_write", Input: json.RawMessage(`{"content":"fixture note"}`)},
		"todoist read":  {ID: "tr", Name: "todoist", Input: json.RawMessage(`{"action":"projects"}`)},
		"todoist write": {ID: "tw", Name: "todoist", Input: json.RawMessage(`{"action":"create","content":"fixture task","labels":["fixture"]}`)},
		"slack read":    {ID: "sr", Name: "slack_search", Input: json.RawMessage(`{"mode":"search","query":"fixture","limit":1}`)},
		"slack write":   {ID: "sw", Name: "slack_post", Input: json.RawMessage(`{"channel":"C_FIXTURE","text":"approved fixture"}`)},
		"choice":        {ID: "ch", Name: "ask_user", Input: json.RawMessage(`{"question":"Which city?","options":["Helsinki","Berlin"]}`)},
		"text":          {ID: "tx", Name: "ask_user", Input: json.RawMessage(`{"question":"Your city?"}`)},
		"http get":      {ID: "hg", Name: "http_request", Input: json.RawMessage(`{"version":1,"request":{"method":"GET","url":"http://public.fixture.test/file","response":{"mode":"download","filename":"fixture.txt"}}}`)},
		"http post":     {ID: "hp", Name: "http_request", Input: json.RawMessage(`{"version":1,"request":{"method":"POST","url":"http://public.fixture.test/submit","response":{"mode":"inline"}}}`)},
	}
	reminderInput, _ := json.Marshal(map[string]string{"action": "create", "title": "REMINDER_CANARY", "due_at": time.Now().Add(time.Second * 2).Format(time.RFC3339)})
	model["reminder"] = llm.ToolCall{ID: "rm", Name: "reminders", Input: reminderInput}
	model["http delete fixture"] = llm.ToolCall{ID: "hd", Name: "http_request", Input: json.RawMessage(`{"version":1,"request":{"method":"GET","url":"http://public.fixture.test/delete-file","response":{"mode":"download","filename":"delete.txt"}}}`)}
	e.Resolve = func(context.Context, string, string) (engine.Profile, error) {
		return engine.Profile{Provider: parityProvider{model, t}, Revision: 1, Digest: "parity-fixture"}, nil
	}
	e.Catalog = assistanttools.Catalog(r)(e)
	e.AccountMemory = browseroauth.PasswordAccounts{Users: h.db}
	token, _ := h.login(t, "owner", "chats:read", "chats:write", "assistant:services:read", "assistant:services:write")
	ctx := tokenContext(token)
	chat := v1.NewChatServiceClient(h.conn)
	extra := v1.NewChatExtrasServiceClient(h.conn)
	c, err := chat.CreateConversation(ctx, &v1.CreateConversationRequest{Client: clientContext(), Operation: operation(), Title: "parity", Settings: &v1.ConversationSettings{ProfileId: "default", MemoryMode: v1.MemoryMode_MEMORY_MODE_ACCOUNT}})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"craft read", "craft write", "todoist read", "todoist write", "slack read", "slack write", "choice", "text", "http get", "http delete fixture", "http post", "reminder"} {
		t.Run(message, func(t *testing.T) {
			turn, sendErr := chat.SendMessage(ctx, &v1.SendMessageRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ClientMessageId: uuid.NewString(), ExpectedHistoryGeneration: 1, Text: message})
			if sendErr != nil {
				t.Fatal(sendErr)
			}
			answered := map[string]bool{}
			done := false
			for until := time.Now().Add(8 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
				snap, snapshotErr := chat.GetSnapshot(ctx, &v1.GetSnapshotRequest{Client: clientContext(), Subscription: &v1.Subscription{Filter: &v1.Subscription_Selected{Selected: &v1.ConversationIDs{ConversationIds: []string{c.ConversationId}}}}})
				if snapshotErr != nil {
					t.Fatal(snapshotErr)
				}
				for _, item := range snap.Conversations {
					for _, p := range item.PendingPrompts {
						if answered[p.Binding.PromptId] {
							continue
						}
						answered[p.Binding.PromptId] = true
						if confirm := p.GetConfirmation(); confirm != nil {
							request := &v1.RespondToConfirmationRequest{Client: clientContext(), Operation: operation(), Binding: p.Binding, SecurityOperationId: confirm.OperationId, Nonce: confirm.Nonce, FingerprintSha256: confirm.FingerprintSha256, Decision: v1.Decision_DECISION_APPROVE, GrantedMaxBytes: confirm.MaxBytes}
							if confirm.MaxBytes > 0 {
								invalid := proto.Clone(request).(*v1.RespondToConfirmationRequest)
								invalid.Operation = operation()
								invalid.GrantedMaxBytes = confirm.MaxBytes + 1
								if _, err = chat.RespondToConfirmation(ctx, invalid); status.Code(err) != codes.InvalidArgument {
									t.Fatal("unbounded byte approval", err)
								}
							}
							if _, err = chat.RespondToPrompt(ctx, &v1.RespondToPromptRequest{Client: clientContext(), Operation: operation(), Binding: p.Binding, TextResponse: "approve"}); status.Code(err) != codes.InvalidArgument {
								t.Fatal("text became security approval", err)
							}
							if _, err = chat.RespondToConfirmation(ctx, request); err != nil {
								t.Fatal(err)
							}
							if _, err = chat.RespondToConfirmation(ctx, request); err != nil {
								t.Fatal("confirmation idempotency", err)
							}
						} else {
							request := &v1.RespondToPromptRequest{Client: clientContext(), Operation: operation(), Binding: p.Binding}
							if message == "choice" {
								request.ChoiceId = "1"
							} else {
								request.TextResponse = "Helsinki"
							}
							if _, err = chat.RespondToPrompt(ctx, request); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				latest, turnErr := chat.GetTurn(ctx, &v1.GetTurnRequest{Client: clientContext(), ConversationId: c.ConversationId, Lookup: &v1.GetTurnRequest_TurnId{TurnId: turn.TurnId}})
				if turnErr != nil {
					t.Fatal(turnErr)
				}
				if latest.State == v1.TurnState_TURN_STATE_COMPLETED {
					done = true
					break
				}
				if latest.State == v1.TurnState_TURN_STATE_FAILED || latest.State == v1.TurnState_TURN_STATE_UNKNOWN_OUTCOME {
					t.Fatal("tool simulator failed", latest.State)
				}
			}
			if !done {
				t.Fatal("simulation timed out")
			}
		})
	}
	if reads.Load() < 2 || writes.Load() != 3 || poster.posts.Load() != 1 || httpReads.Load() != 2 || httpWrites.Load() != 1 {
		t.Fatal("service dispatch counts", reads.Load(), writes.Load(), poster.posts.Load(), httpReads.Load(), httpWrites.Load())
	}
	// No native control stream or WatchEvents subscriber is connected. Delivery
	// must still appear once in the durable transcript for later reconnection.
	accountDB, openErr := sqlitedb.Open(context.Background(), h.paths[0], false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, updateErr := accountDB.ExecContext(context.Background(), `UPDATE mobile_reminders SET due_at=? WHERE owner_id=? AND state='pending'`, time.Now().Add(-time.Second).UnixMilli(), owner); updateErr != nil {
		t.Fatal(updateErr)
	}
	if closeErr := accountDB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	reminderCtx, stopReminders := context.WithCancel(context.Background())
	defer stopReminders()
	remindersDone := make(chan struct{})
	go func() { e.RunReminders(reminderCtx); close(remindersDone) }()
	defer func() { stopReminders(); <-remindersDone }()
	delivered := false
	for until := time.Now().Add(6 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
		messages, listErr := chat.ListMessages(ctx, &v1.ListMessagesRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1, PageSize: 100})
		if listErr != nil {
			t.Fatal(listErr)
		}
		count := 0
		for _, m := range messages.Messages {
			if strings.Contains(m.Text, "Напоминание: REMINDER_CANARY") {
				count++
			}
		}
		if count > 1 {
			t.Fatal("reminder delivered more than once")
		}
		if count == 1 {
			delivered = true
			break
		}
	}
	if !delivered {
		t.Fatal("native reminder was not delivered without a client stream")
	}
	list, err := extra.ListHTTPArtifacts(ctx, &v1.ListHTTPArtifactsRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1})
	if err != nil || len(list.Artifacts) != 2 {
		t.Fatal("download missing", list, err)
	}
	var artifact, eraseArtifact *v1.HTTPArtifactInfo
	for _, item := range list.Artifacts {
		if item.Filename == "delete.txt" {
			eraseArtifact = item
		} else {
			artifact = item
		}
	}
	if artifact == nil || eraseArtifact == nil {
		t.Fatal("download metadata not retained")
	}
	deletion := &v1.DeleteHTTPArtifactRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1, ArtifactId: eraseArtifact.ArtifactId}
	for n := 0; n < 2; n++ {
		if _, deleteErr := extra.DeleteHTTPArtifact(ctx, deletion); deleteErr != nil {
			t.Fatal("idempotent artifact delete", deleteErr)
		}
	}
	read := &v1.ReadHTTPArtifactRequest{Client: clientContext(), ConversationId: c.ConversationId, HistoryGeneration: 1, ArtifactId: artifact.ArtifactId, MaxBytes: 64 << 10}
	body, err := extra.ReadHTTPArtifact(ctx, read)
	if err != nil || string(body.Chunk) != "HTTP_ARTIFACT_CANARY" || !body.Eof {
		t.Fatal("artifact read", body, err)
	}
	// Native artifacts are never exposed through dashboard or the generic store.
	if _, err = artifacts.Get(context.Background(), owner, artifact.ArtifactId); err == nil {
		t.Fatal("legacy artifact route exposed native attachment")
	}
	other, _ := h.login(t, "other", "chats:read", "chats:write")
	if _, err = extra.ReadHTTPArtifact(tokenContext(other), read); status.Code(err) != codes.NotFound {
		t.Fatal("foreign artifact", err)
	}
	if _, err = chat.ClearHistory(ctx, &v1.ClearHistoryRequest{Client: clientContext(), Operation: operation(), ConversationId: c.ConversationId, ExpectedHistoryGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = extra.ReadHTTPArtifact(ctx, read); err == nil {
		t.Fatal("cleared artifact still readable")
	}
	read.HistoryGeneration = 2
	if _, err = extra.ReadHTTPArtifact(ctx, read); status.Code(err) != codes.NotFound {
		t.Fatal("old artifact rebound to new generation", err)
	}
}
