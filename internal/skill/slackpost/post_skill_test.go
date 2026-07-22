package slackpost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	slackch "github.com/iulita-ai/iulita/internal/channel/slack"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// --- fakes ---

type fakePoster struct {
	mode    string
	postErr error
	posts   []string
}

func (f *fakePoster) WriteMode(_ context.Context, _ string) string { return f.mode }
func (f *fakePoster) PostToChannel(_ context.Context, _, text string) (string, error) {
	if f.postErr != nil {
		return "", f.postErr
	}
	f.posts = append(f.posts, text)
	return "1700000000.000100", nil
}

type fakeAudit struct{ actions []string }

func (f *fakeAudit) SaveAuditEntry(_ context.Context, e *domain.AuditEntry) error {
	f.actions = append(f.actions, e.Action)
	return nil
}

type fakeAsker struct {
	answer string
	err    error
	called bool
}

func (f *fakeAsker) Ask(_ context.Context, _ string, _ []interact.Option) (string, error) {
	f.called = true
	return f.answer, f.err
}

func newSkill(p ChannelPoster) (*PostSkill, *fakeAudit) {
	a := &fakeAudit{}
	s := NewPostSkill(a, zap.NewNop())
	s.SetChannelPoster(p)
	return s, a
}

func run(t *testing.T, s *PostSkill, ctx context.Context, in map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := s.Execute(ctx, raw)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	return out
}

func lastAudit(a *fakeAudit) string {
	if len(a.actions) == 0 {
		return ""
	}
	return a.actions[len(a.actions)-1]
}

// --- tests ---

func TestPost_Denied(t *testing.T) {
	p := &fakePoster{mode: "off"}
	s, a := newSkill(p)
	out := run(t, s, context.Background(), map[string]any{"channel": "C9", "text": "hi"})
	if !strings.Contains(out, "isn't allowed") {
		t.Errorf("expected denial, got %q", out)
	}
	if len(p.posts) != 0 {
		t.Error("must not post to a non-writable channel")
	}
	if lastAudit(a) != "slack.post.denied" {
		t.Errorf("audit = %q", lastAudit(a))
	}
}

func TestPost_DraftApprove(t *testing.T) {
	p := &fakePoster{mode: "draft"}
	s, a := newSkill(p)
	asker := &fakeAsker{answer: "approve"}
	ctx := interact.WithPrompter(context.Background(), asker)
	out := run(t, s, ctx, map[string]any{"channel": "C1", "text": "deploy green"})
	if !asker.called {
		t.Error("draft mode must ask for approval")
	}
	if len(p.posts) != 1 || p.posts[0] != "deploy green" {
		t.Errorf("expected the approved text posted, got %v", p.posts)
	}
	if !strings.Contains(out, "Posted") || lastAudit(a) != "slack.post.sent" {
		t.Errorf("out=%q audit=%q", out, lastAudit(a))
	}
}

func TestPost_DraftDiscard(t *testing.T) {
	p := &fakePoster{mode: "draft"}
	s, a := newSkill(p)
	ctx := interact.WithPrompter(context.Background(), &fakeAsker{answer: "discard"})
	out := run(t, s, ctx, map[string]any{"channel": "C1", "text": "x"})
	if len(p.posts) != 0 {
		t.Error("discarded draft must not post")
	}
	if !strings.Contains(out, "discarded") || lastAudit(a) != "slack.post.discarded" {
		t.Errorf("out=%q audit=%q", out, lastAudit(a))
	}
}

func TestPost_DraftNoPrompterFailsClosed(t *testing.T) {
	p := &fakePoster{mode: "draft"}
	s, a := newSkill(p)
	// No prompter in ctx → interact.PrompterFrom returns NoopAsker → ErrNoPrompter.
	out := run(t, s, context.Background(), map[string]any{"channel": "C1", "text": "x"})
	if len(p.posts) != 0 {
		t.Error("must not post without an approval channel")
	}
	if !strings.Contains(out, "didn't post") || lastAudit(a) != "slack.post.approval_failed" {
		t.Errorf("out=%q audit=%q", out, lastAudit(a))
	}
}

func TestPost_Auto(t *testing.T) {
	p := &fakePoster{mode: "auto"}
	s, a := newSkill(p)
	run(t, s, context.Background(), map[string]any{"channel": "C1", "text": "auto post"})
	if len(p.posts) != 1 {
		t.Error("auto mode should post directly")
	}
	if lastAudit(a) != "slack.post.sent" {
		t.Errorf("audit = %q", lastAudit(a))
	}
}

func TestPost_AutoGuardrailBlocked(t *testing.T) {
	p := &fakePoster{mode: "auto", postErr: slackch.ErrGuardrailBlocked}
	s, a := newSkill(p)
	out := run(t, s, context.Background(), map[string]any{"channel": "C1", "text": "x"})
	if !strings.Contains(out, "guardrail") || lastAudit(a) != "slack.post.blocked_guardrail" {
		t.Errorf("out=%q audit=%q", out, lastAudit(a))
	}
}

func TestPost_ProvenanceForcesDraft(t *testing.T) {
	p := &fakePoster{mode: "auto"} // channel is auto...
	s, _ := newSkill(p)
	asker := &fakeAsker{answer: "approve"}
	ctx := interact.WithPrompter(context.Background(), asker)
	run(t, s, ctx, map[string]any{"channel": "C1", "text": "x", "provenance": "from #eng by @bob"})
	if !asker.called {
		t.Error("provenance must force draft approval even on an auto channel")
	}
}

// The load-bearing security test: content from slack_search this turn forces
// draft approval even on an auto channel and even with NO provenance declared
// (the LLM could omit it under injection; the server taint can't be prompted away).
func TestPost_SearchTaintForcesDraft(t *testing.T) {
	p := &fakePoster{mode: "auto"}
	s, _ := newSkill(p)
	asker := &fakeAsker{answer: "discard"}
	ctx := skill.WithTurnTaint(context.Background())
	skill.MarkSlackSearchUsed(ctx)
	ctx = interact.WithPrompter(ctx, asker)
	run(t, s, ctx, map[string]any{"channel": "C1", "text": "x"}) // no provenance
	if !asker.called {
		t.Fatal("search-tainted turn must force draft approval on an auto channel")
	}
	if len(p.posts) != 0 {
		t.Error("discarded tainted draft must not post")
	}
}

func TestPost_SecretRefused(t *testing.T) {
	p := &fakePoster{mode: "auto"}
	s, a := newSkill(p)
	out := run(t, s, context.Background(), map[string]any{"channel": "C1", "text": "key AKIAIOSFODNN7EXAMPLE here"}) // gitleaks:allow (test fixture)
	if len(p.posts) != 0 {
		t.Error("must not post text with a secret")
	}
	if !strings.Contains(out, "credential") || lastAudit(a) != "slack.post.blocked_secret" {
		t.Errorf("out=%q audit=%q", out, lastAudit(a))
	}
}

func TestPost_NoPoster(t *testing.T) {
	s := NewPostSkill(&fakeAudit{}, zap.NewNop())
	raw, _ := json.Marshal(map[string]any{"channel": "C1", "text": "x"})
	out, err := s.Execute(context.Background(), raw)
	if err != nil || !strings.Contains(out, "not available") {
		t.Errorf("out=%q err=%v", out, err)
	}
}

func TestPost_PublishesEvent(t *testing.T) {
	p := &fakePoster{mode: "auto"}
	s, _ := newSkill(p)
	bus := eventbus.New(zap.NewNop())
	var got eventbus.SlackPostPayload
	n := 0
	bus.Subscribe(eventbus.SlackPost, func(_ context.Context, evt eventbus.Event) error {
		if pl, ok := evt.Payload.(eventbus.SlackPostPayload); ok {
			got = pl
			n++
		}
		return nil
	})
	s.SetBus(bus)

	run(t, s, context.Background(), map[string]any{"channel": "C1", "text": "auto post"})
	if n != 1 || got.Mode != "auto" || got.Decision != "auto" || !got.Success {
		t.Errorf("published %+v (n=%d), want {auto auto true}", got, n)
	}

	// A denial publishes a non-success event with the decision as the kind.
	deny := &fakePoster{mode: "off"}
	s2, _ := newSkill(deny)
	got = eventbus.SlackPostPayload{}
	n = 0
	s2.SetBus(bus)
	run(t, s2, context.Background(), map[string]any{"channel": "C9", "text": "x"})
	if n != 1 || got.Success || got.Decision != "denied" {
		t.Errorf("denial published %+v (n=%d), want denied/false", got, n)
	}
}

func TestPost_Metadata(t *testing.T) {
	s := NewPostSkill(nil, nil)
	if s.Name() != "slack_post" {
		t.Errorf("name = %q", s.Name())
	}
	if got := s.RequiredCapabilities(); len(got) != 1 || got[0] != "slack_write" {
		t.Errorf("caps = %v", got)
	}
	if s.RequestTimeout() <= 30*60*1e9 {
		t.Error("timeout must exceed the 30m prompt timeout")
	}
	var schema map[string]any
	if err := json.Unmarshal(s.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid: %v", err)
	}
}
