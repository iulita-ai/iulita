package telegrambusiness

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/models"
)

type profileBoundaryProvider struct {
	before   func()
	after    func()
	requests []llm.Request
	response llm.Response
	attempts int
}

func (p *profileBoundaryProvider) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if p.before != nil {
		p.before()
	}
	if req.BeforeRequest == nil {
		return llm.Response{}, errors.New("missing admission guard")
	}
	if checkErr1 := req.BeforeRequest(ctx); checkErr1 != nil {
		return llm.Response{}, checkErr1
	}
	p.attempts++
	p.requests = append(p.requests, req)
	if p.after != nil {
		p.after()
	}
	return p.response, nil
}

type profileBoundaryRepo struct {
	*memoryRepository
	debits []SourceBudgetRequest
}

func (r *profileBoundaryRepo) ReserveSource(_ context.Context, req SourceBudgetRequest) error {
	r.debits = append(r.debits, req)
	return nil
}

type profileBoundarySource struct {
	generation string
	calls      int
	process    func(context.Context, Scope, []Message) (ProcessedSource, error)
}

func (s *profileBoundarySource) BusinessGeneration(Scope) string { return s.generation }

func (*profileBoundarySource) Descriptor(Scope) string { return "fixed-local-source" }
func (s *profileBoundarySource) Process(ctx context.Context, scope Scope, messages []Message) (ProcessedSource, error) {
	s.calls++
	if s.process != nil {
		return s.process(ctx, scope, messages)
	}
	return ProcessedSource{}, nil
}
func profileBoundarySnapshot(t *testing.T, p llm.Provider, generation string) *llm.ProfileSnapshot {
	t.Helper()
	settings := models.Settings{SchemaVersion: 1, Profiles: models.CandidateProfiles(), Policy: models.Policy{Everyday: "ds-flash"}}
	bindings := map[string]llm.ProfileBinding{}
	for _, profile := range settings.Profiles {
		bindings[profile.ID] = llm.ProfileBinding{Profile: profile, Provider: p, Eligibility: "production_eligible", Endpoint: "https://model.invalid/v1", ConnectionGeneration: generation}
	}
	snapshot, err := llm.NewProfileSnapshot(1, settings, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type profileBoundaryFixture struct {
	repo      *profileBoundaryRepo
	provider  *profileBoundaryProvider
	source    *profileBoundarySource
	generator *ProfileGenerator
	snapshot  *llm.ProfileSnapshot
	scope     Scope
}

func newProfileBoundary(t *testing.T, media bool) *profileBoundaryFixture {
	t.Helper()
	f := &profileBoundaryFixture{
		repo:     &profileBoundaryRepo{memoryRepository: &memoryRepository{states: map[Key]State{}}},
		provider: &profileBoundaryProvider{response: llm.Response{Content: "A bounded reply", FinishReason: "stop"}},
		source:   &profileBoundarySource{generation: "delivery"},
		scope:    Scope{Key: Key{InstanceID: "instance", BotID: 7, ConnectionID: "connection"}, OwnerID: "owner", PeerID: 99},
	}
	f.snapshot = profileBoundarySnapshot(t, f.provider, "key-v1")
	f.generator = NewProfileGenerator(f.repo, func() *llm.ProfileSnapshot { return f.snapshot }, f.source)
	label, ok := f.generator.Descriptor()
	if !ok {
		t.Fatal("descriptor unavailable")
	}
	policy := PeerPolicy{Enabled: true, Consent: true, Provider: label, Mode: ModeDraft, TokenOnly: true, MediaConsent: media, MediaProvider: "fixed-local-source"}
	f.repo.states[f.scope.Key] = State{
		Connection: Connection{RemoteConnection: RemoteConnection{Key: f.scope.Key, Enabled: true, OwnerTelegramID: 42, OwnerChatID: 42, Rights: Rights{CanReply: true}}, OwnerID: f.scope.OwnerID, Epoch: 4, RuntimeGeneration: "runtime", RestoreGeneration: "restore", DeliveryGeneration: "delivery", Policy: policy},
		Peers:      map[int64]*Peer{99: {ID: 99, Revision: 8, Policy: policy, Messages: []Message{{ID: 10, Date: time.Now().UTC(), Revision: 2, Direction: "incoming", Text: "the selected peer request"}}}, 100: {ID: 100, Messages: []Message{{Text: "OTHER-PEER-PRIVATE"}}}},
	}
	return f
}
func (f *profileBoundaryFixture) settings(operation string) GenerationSettings {
	label, _ := f.generator.Descriptor()
	return GenerationSettings{Provider: label, MaxInputTokens: 16000, MaxOutputTokens: 1000, Operation: operation, Epoch: 4, ContextRevision: 8, RuntimeGeneration: "runtime", RestoreGeneration: "restore"}
}
func (f *profileBoundaryFixture) generate(ctx context.Context, operation string) (string, error) {
	state, err := f.repo.Get(ctx, f.scope.Key)
	if err != nil {
		return "", err
	}
	return f.generator.Generate(context.WithValue(ctx, generationKey{}, f.settings(operation)), f.scope, contextMessages(state.Peers[99]))
}
func (f *profileBoundaryFixture) change(t *testing.T, fn func(*State)) {
	t.Helper()
	if checkErr2 := f.repo.Update(context.Background(), f.scope.Key, func(state *State) error { fn(state); return nil }); checkErr2 != nil {
		t.Fatal(checkErr2)
	}
}
func TestBusinessProfileGeneratorScopeAndSingleAdmission(t *testing.T) {
	f := newProfileBoundary(t, false)
	if _, checkErr3 := f.generate(context.Background(), "draft"); checkErr3 != nil {
		t.Fatal(checkErr3)
	}
	if f.provider.attempts != 1 || len(f.provider.requests) != 1 {
		t.Fatal("provider not admitted exactly once")
	}
	req := f.provider.requests[0]
	if !strings.Contains(req.Message, "the selected peer request") || strings.Contains(req.Message, "OTHER-PEER-PRIVATE") || len(req.Tools) != 0 || len(req.History) != 0 || len(req.ToolExchanges) != 0 || len(req.Documents) != 0 || req.StaticSystemPrompt != "" {
		t.Fatalf("scope/tool boundary failed: %+v", req)
	}
	if !req.SingleAttempt || req.MaxOutputTokens != 1000 || !req.PinnedProfile || req.UserID != f.scope.OwnerID {
		t.Fatal("bounded pinned request lost")
	}
}
func TestBusinessProfileGeneratorRevalidatesAtHTTPAdmission(t *testing.T) {
	for _, name := range []string{"owner", "connection-consent", "peer-consent", "pause", "runtime", "peer-revision", "key", "cancel", "peer-media-consent", "connection-media-consent", "runtime-stop", "channel-replacement", "persisted-delivery"} {
		t.Run(name, func(t *testing.T) {
			withMedia := name != "runtime-stop" && name != "channel-replacement" && name != "persisted-delivery"
			f := newProfileBoundary(t, withMedia)
			f.change(t, func(st *State) {
				st.Peers[99].Messages[0].ExtractedText = "previously consented transcript"
				st.Peers[99].Messages[0].ExtractedProvider = "fixed-local-source"
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.provider.before = func() {
				if name == "runtime-stop" {
					f.source.generation = ""
					return
				}
				if name == "channel-replacement" {
					f.source.generation = "replacement-delivery"
					return
				}
				if name == "key" {
					f.snapshot = profileBoundarySnapshot(t, f.provider, "key-v2")
					return
				}
				if name == "cancel" {
					cancel()
					return
				}
				f.change(t, func(st *State) {
					switch name {
					case "persisted-delivery":
						st.Connection.DeliveryGeneration = "replacement-delivery"
					case "owner":
						st.Connection.OwnerID = "another-owner"
					case "connection-consent":
						st.Connection.Policy.Consent = false
					case "peer-consent":
						st.Peers[99].Policy.Consent = false
					case "peer-media-consent":
						st.Peers[99].Policy.MediaConsent = false
					case "connection-media-consent":
						st.Connection.Policy.MediaConsent = false
					case "pause":
						st.Connection.Paused = true
					case "runtime":
						st.Connection.RuntimeGeneration = "replacement"
					case "peer-revision":
						st.Peers[99].Revision++
					}
				})
			}
			if _, checkErr4 := f.generate(ctx, "draft"); checkErr4 == nil || f.provider.attempts != 0 {
				t.Fatalf("revoked HTTP admitted: %v, %d", checkErr4, f.provider.attempts)
			}
		})
	}
}
func TestBusinessProfileGeneratorSourceBudgetCacheAndFollowingText(t *testing.T) {
	for _, kind := range []string{"document", "voice"} {
		t.Run(kind, func(t *testing.T) {
			f := newProfileBoundary(t, true)
			f.change(t, func(st *State) { st.Peers[99].Messages[0].Media = []Media{{Kind: kind, FileID: "source"}} })
			parses := 0
			f.source.process = func(ctx context.Context, scope Scope, messages []Message) (ProcessedSource, error) {
				if checkErr5 := CheckSourceGuard(ctx); checkErr5 != nil {
					return ProcessedSource{}, checkErr5
				}
				for _, message := range messages {
					if len(message.Media) == 0 || message.ExtractedText != "" {
						continue
					}
					parses++
					if kind == "voice" {
						if checkErr6 := ReserveSource(ctx, scope, 32, 120); checkErr6 != nil {
							return ProcessedSource{}, checkErr6
						}
					}
					return ProcessedSource{Text: "local extracted source", Extractions: []SourceExtraction{{MessageID: message.ID, Revision: message.Revision, Text: "local extracted source"}}}, nil
				}
				return ProcessedSource{}, nil
			}
			if _, checkErr7 := f.generate(context.Background(), "draft"); checkErr7 != nil {
				t.Fatal(checkErr7)
			}
			f.change(t, func(st *State) {
				st.Peers[99].Revision++
				st.Peers[99].Messages = append(st.Peers[99].Messages, Message{ID: 11, Date: time.Now().UTC(), Revision: 3, Direction: "incoming", Text: "following text"})
			})
			// A new immutable turn captures the updated peer revision.
			st, err := f.repo.Get(context.Background(), f.scope.Key)
			if err != nil {
				t.Fatal(err)
			}
			settings := f.settings("draft")
			settings.ContextRevision = 9
			if _, err = f.generator.Generate(context.WithValue(context.Background(), generationKey{}, settings), f.scope, contextMessages(st.Peers[99])); err != nil {
				t.Fatal(err)
			}
			if parses != 1 || !strings.Contains(f.provider.requests[1].Message, "local extracted source") || !strings.Contains(f.provider.requests[1].Message, "following text") {
				t.Fatal("cached extraction was reparsed or lost")
			}
			if kind == "voice" && (len(f.repo.debits) != 1 || f.repo.debits[0].Scope != f.scope || f.repo.debits[0].DurationSeconds != 120) {
				t.Fatal("source budget not forwarded exactly once")
			}
		})
	}
}
func TestBusinessProfileGeneratorCannotRestoreDeletedExpiredOrRevokedSources(t *testing.T) {
	for _, name := range []string{"deleted", "expired", "peer-media-revoked", "connection-media-revoked"} {
		t.Run(name, func(t *testing.T) {
			f := newProfileBoundary(t, true)
			f.change(t, func(st *State) { st.Peers[99].Messages[0].Media = []Media{{Kind: "document", FileID: "source"}} })
			f.source.process = func(context.Context, Scope, []Message) (ProcessedSource, error) {
				f.change(t, func(st *State) {
					switch name {
					case "deleted":
						st.Peers[99].Messages[0].Deleted = true
					case "expired":
						st.Peers[99].Messages[0].Date = time.Now().UTC().Add(-8 * 24 * time.Hour)
					case "peer-media-revoked":
						st.Peers[99].Policy.MediaConsent = false
					case "connection-media-revoked":
						st.Connection.Policy.MediaConsent = false
					}
				})
				return ProcessedSource{Extractions: []SourceExtraction{{MessageID: 10, Revision: 2, Text: "late extraction must not resurrect"}}}, nil
			}
			if _, checkErr8 := f.generate(context.Background(), "draft"); checkErr8 == nil || f.provider.attempts != 0 {
				t.Fatal("stale source admitted")
			}
			st, err := f.repo.Get(context.Background(), f.scope.Key)
			if err != nil {
				t.Fatal(err)
			}
			if st.Peers[99].Messages[0].ExtractedText != "" {
				t.Fatal("stale extracted content persisted")
			}
		})
	}
}
func TestBusinessProfileGeneratorCompactRejectsPartialOrToolOutput(t *testing.T) {
	for _, reason := range []string{"", "length", "max_tokens", "content_filter", "tool_calls"} {
		t.Run(reason, func(t *testing.T) {
			f := newProfileBoundary(t, false)
			f.provider.response.FinishReason = reason
			if reason == "tool_calls" {
				f.provider.response.ToolCalls = []llm.ToolCall{{ID: "untrusted-tool"}}
			}
			f.change(t, func(st *State) { st.Peers[99].Messages[0].Text = strings.Repeat("source facts ", 400) })
			st, err := f.repo.Get(context.Background(), f.scope.Key)
			if err != nil {
				t.Fatal(err)
			}
			svc := New(f.repo, &testAPI{remote: st.Connection.RemoteConnection}, f.generator, Options{RuntimeGeneration: "runtime", RestoreGeneration: "restore", Budget: &testBudget{}})
			if _, err = svc.Compact(context.Background(), f.scope); err == nil {
				t.Fatal("partial compaction accepted")
			}
			after, err := f.repo.Get(context.Background(), f.scope.Key)
			if err != nil {
				t.Fatal(err)
			}
			if after.Peers[99].Summary != nil || after.Peers[99].Messages[0].Text != st.Peers[99].Messages[0].Text {
				t.Fatal("failed compaction changed context")
			}
			if f.provider.attempts != 1 || !strings.Contains(f.provider.requests[0].SystemPrompt, "Summarize") {
				t.Fatal("compaction provider path not exercised")
			}
		})
	}
}
func TestBusinessProfileGeneratorOldPhotoDoesNotBlockLatestText(t *testing.T) {
	f := newProfileBoundary(t, true)
	f.change(t, func(st *State) {
		st.Peers[99].Messages[0].Media = []Media{{Kind: "photo", FileID: "old-photo"}}
		st.Peers[99].Messages = append(st.Peers[99].Messages, Message{ID: 11, Revision: 3, Date: time.Now().UTC(), Direction: "incoming", Text: "latest text only"})
	})
	if _, checkErr9 := f.generate(context.Background(), "draft"); checkErr9 != nil {
		t.Fatalf("old photo blocked text-only request: %v", checkErr9)
	}
	if f.provider.attempts != 1 || len(f.provider.requests[0].Images) != 0 {
		t.Fatal("text-only profile received historical photo")
	}
	f.change(t, func(st *State) {
		st.Peers[99].Messages[1].Media = []Media{{Kind: "photo", FileID: "current-photo"}}
	})
	if _, generationErr := f.generate(context.Background(), "draft"); !errors.Is(generationErr, ErrUnsupported) || f.provider.attempts != 1 {
		t.Fatalf("current photo bypassed text-only profile: %v", generationErr)
	}
}

type profileSourceWithoutRuntime struct{ source SourceProcessor }

func (s profileSourceWithoutRuntime) Descriptor(scope Scope) string {
	return s.source.Descriptor(scope)
}
func (s profileSourceWithoutRuntime) Process(ctx context.Context, scope Scope, messages []Message) (ProcessedSource, error) {
	return s.source.Process(ctx, scope, messages)
}
func TestBusinessProfileGeneratorRequiresPublishedRuntimeEvenForText(t *testing.T) {
	for _, name := range []string{"no-port", "stopped", "wrong-generation"} {
		t.Run(name, func(t *testing.T) {
			f := newProfileBoundary(t, false)
			switch name {
			case "no-port":
				f.generator.sources = profileSourceWithoutRuntime{source: f.source}
			case "stopped":
				f.source.generation = ""
			case "wrong-generation":
				f.source.generation = "another-generation"
			}
			if _, generationErr := f.generate(context.Background(), "draft"); generationErr == nil || f.provider.attempts != 0 {
				t.Fatalf("text-only request admitted without exact live runtime: %v", generationErr)
			}
		})
	}
}

func TestBusinessProfileGeneratorStopAfterHTTPDoesNotPublishDraft(t *testing.T) {
	for _, replacement := range []string{"", "replacement-delivery"} {
		t.Run("generation-"+replacement, func(t *testing.T) {
			f := newProfileBoundary(t, false)
			f.provider.after = func() { f.source.generation = replacement }
			st, err := f.repo.Get(context.Background(), f.scope.Key)
			if err != nil {
				t.Fatal(err)
			}
			svc := New(f.repo, &testAPI{remote: st.Connection.RemoteConnection}, f.generator, Options{RuntimeGeneration: "runtime", RestoreGeneration: "restore", Budget: &testBudget{}})
			draft, draftErr := svc.Draft(context.Background(), f.scope)
			if draftErr == nil || draft != nil || f.provider.attempts != 1 {
				t.Fatalf("late stopped-provider response published: draft=%v err=%v admissions=%d", draft, draftErr, f.provider.attempts)
			}
			after, readErr := f.repo.Get(context.Background(), f.scope.Key)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(after.Peers[99].Drafts) != 0 || after.Peers[99].Summary != nil {
				t.Fatal("late response resurrected derived content")
			}
		})
	}
}

func TestBusinessProfileGeneratorStopDuringBindingClosesHTTPAdmission(t *testing.T) {
	f := newProfileBoundary(t, false)
	stopDuringBinding := false
	f.generator.snapshot = func() *llm.ProfileSnapshot {
		if stopDuringBinding {
			stopDuringBinding = false
			f.source.generation = ""
		}
		return f.snapshot
	}
	f.provider.before = func() { stopDuringBinding = true }
	if _, generationErr := f.generate(context.Background(), "draft"); generationErr == nil || f.provider.attempts != 0 {
		t.Fatalf("runtime stopped during binding admitted HTTP: %v", generationErr)
	}
}
