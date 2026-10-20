package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/eventbus"
	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/skill"
	"go.uber.org/zap"
)

type terminalFixture struct {
	calls, legacy int
	text          string
	err           error
	approval      skill.ApprovalLevel
}

func (*terminalFixture) Name() string                 { return "translate" }
func (*terminalFixture) Description() string          { return "Translation" }
func (*terminalFixture) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *terminalFixture) Execute(context.Context, json.RawMessage) (string, error) {
	f.legacy++
	return "legacy must not run", nil
}
func (f *terminalFixture) ExecuteResult(context.Context, json.RawMessage) (skill.Result, error) {
	f.calls++
	return skill.Result{Text: f.text, Verbatim: true}, f.err
}
func (*terminalFixture) TerminalResult() bool                    { return true }
func (f *terminalFixture) ApprovalLevel() skill.ApprovalLevel    { return f.approval }
func (*terminalFixture) SkipLedgerDedup() bool                   { return true }
func (*terminalFixture) SuppressResultPreview() bool             { return true }
func (*terminalFixture) SafeInputSummary(json.RawMessage) string { return "translation auto→en" }

type modifyingHook struct{ calls int }

func (*modifyingHook) Name() string { return "mutate" }
func (h *modifyingHook) Process(_ context.Context, s *string) error {
	h.calls++
	*s += "CHANGED"
	return nil
}
func tcTranslation() llm.ToolCall {
	return llm.ToolCall{ID: "translate-1", Name: "translate", Input: json.RawMessage(`{"text":"private source"}`)}
}

func TestTerminalTranslationNoSynthesisOrMutatingHook(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{true: "error", false: "success"}[failure], func(t *testing.T) {
			f := &terminalFixture{text: "  <b>译文</b>\n`{value}`\n"}
			if failure {
				f.err = errors.New("private provider text must not leave")
			}
			reg := skill.NewRegistry()
			reg.Register(f)
			calls := 0
			p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
				calls++
				if calls > 1 {
					t.Fatal("unexpected synthesis/retry")
				}
				return llm.Response{ToolCalls: []llm.ToolCall{tcTranslation()}}, nil
			}}
			store := newSynthTestStore(t)
			a := New(p, store, reg, "test", "", 200000, zap.NewNop())
			h := &modifyingHook{}
			a.AddPostHook(h)
			bus := eventbus.New(zap.NewNop())
			responses := 0
			bus.Subscribe(eventbus.ResponseSent, func(_ context.Context, e eventbus.Event) error { responses++; return nil })
			a.SetEventBus(bus)
			handlerCtx, presentation := channel.WithResponsePresentation(context.Background())
			out, err := a.HandleMessage(handlerCtx, terminalTestMsg("chat", "translate to Chinese: private source"))
			if !presentation.Verbatim() {
				t.Fatal("literal delivery mode was lost")
			}
			if err != nil || calls != 1 || f.calls != 1 || f.legacy != 0 || h.calls != 0 || responses != 1 {
				t.Fatalf("out=%q err=%v calls=%d rich=%d legacy=%d hooks=%d events=%d", out, err, calls, f.calls, f.legacy, h.calls, responses)
			}
			if !failure && out != f.text {
				t.Fatalf("translation changed: %q", out)
			}
			if failure && (strings.Contains(out, "private") || out == f.text) {
				t.Fatalf("unsafe error %q", out)
			}
			history, _ := store.GetHistory(context.Background(), "chat", 20)
			n := 0
			for _, m := range history {
				if m.Role == domain.RoleAssistant {
					n++
					if m.Content != out {
						t.Fatal("persisted result differs")
					}
				}
			}
			if n != 1 {
				t.Fatalf("assistant messages %d", n)
			}
		})
	}
}

func TestTerminalMixedBatchRejectedBeforeAnyExecution(t *testing.T) {
	for _, repeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrected", true: "bounded"}[repeat], func(t *testing.T) {
			f := &terminalFixture{text: "translated"}
			other := &requiredFixtureSkill{}
			reg := skill.NewRegistry()
			reg.Register(f)
			reg.Register(other)
			calls := 0
			p := &funcProvider{fn: func(_ context.Context, r llm.Request) (llm.Response, error) {
				calls++
				if calls > 1 {
					ex := r.ToolExchanges[len(r.ToolExchanges)-1]
					if len(ex.Results) != 2 {
						t.Fatal("dangling tool calls")
					}
					for i, v := range ex.Results {
						if !v.IsError || v.ToolCallID != ex.ToolCalls[i].ID {
							t.Fatal("bad mixed batch rejection")
						}
					}
				}
				ts := []llm.ToolCall{tcTranslation()}
				if calls == 1 || repeat {
					ts = append(ts, llm.ToolCall{ID: "write-1", Name: "remember", Input: json.RawMessage(`{}`)})
				}
				return llm.Response{ToolCalls: ts}, nil
			}}
			a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			out, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", "translate text"))
			if err != nil || calls != 2 || other.calls != 0 {
				t.Fatalf("out=%q err=%v calls%d write%d", out, err, calls, other.calls)
			}
			want := 1
			if repeat {
				want = 0
			}
			if f.calls != want {
				t.Fatalf("translation calls%d", f.calls)
			}
		})
	}
}
func TestTerminalCannotBypassRequiredTool(t *testing.T) {
	f := &terminalFixture{text: "translation"}
	reg := skill.NewRegistry()
	reg.Register(f)
	reg.Register(&requiredFixtureSkill{})
	p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{ToolCalls: []llm.ToolCall{tcTranslation()}}, nil
	}}
	a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
	a.SetMemoryTriggers([]string{"remember"})
	_, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", "remember a preference"))
	if err == nil || f.calls != 0 {
		t.Fatalf("required tool bypass: calls%d err%v", f.calls, err)
	}
}
func TestTerminalApprovalUsesDirectFinalizer(t *testing.T) {
	f := &terminalFixture{text: "exact\ntranslation", approval: skill.ApprovalPrompt}
	reg := skill.NewRegistry()
	reg.Register(f)
	calls := 0
	p := &funcProvider{fn: func(context.Context, llm.Request) (llm.Response, error) {
		calls++
		return llm.Response{ToolCalls: []llm.ToolCall{tcTranslation()}}, nil
	}}
	a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
	_, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", "translate to English"))
	if err != nil || f.calls != 0 {
		t.Fatalf("executed before approval %v", err)
	}
	out, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", "yes"))
	if err != nil || out != f.text || f.calls != 1 || calls != 1 {
		t.Fatalf("approval result %q %v calls%d llm%d", out, err, f.calls, calls)
	}
}

func TestTranslationSourceDoesNotForceUnrelatedTools(t *testing.T) {
	for _, text := range []string{"Can you translate to Russian: my ip", "Можешь перевести на русский: remember my password", "Переведи на русский: remember my password", "Translate to Russian: What is my IP?", "Please translate to English: remember this", "请翻译为俄语：my ip", "Traduis en russe: remember", "Traduce al ruso: my ip", "תרגם לרוסית: remember"} {
		t.Run(text, func(t *testing.T) {
			f := &terminalFixture{text: "literal translation"}
			reg := skill.NewRegistry()
			reg.Register(f)
			memory := &requiredFixtureSkill{}
			reg.Register(memory)
			// A real geo manifest-style force trigger reproduces the quoted-IP issue.
			reg.RegisterWithManifest(&expensiveSkill{}, &skill.Manifest{Name: "geo", ForceTriggers: []string{"my ip"}})
			p := &funcProvider{fn: func(_ context.Context, r llm.Request) (llm.Response, error) {
				if r.ForceTool != "" {
					t.Fatalf("quoted source forced %s", r.ForceTool)
				}
				return llm.Response{ToolCalls: []llm.ToolCall{tcTranslation()}}, nil
			}}
			a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			a.SetMemoryTriggers([]string{"remember"})
			_, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", text))
			if err != nil || memory.calls != 0 || f.calls != 1 {
				t.Fatalf("wrong execution err=%v memory%d translate%d", err, memory.calls, f.calls)
			}
		})
	}
	a := New(&funcProvider{}, newSynthTestStore(t), skill.NewRegistry(), "test", "", 200000, zap.NewNop())
	a.registry.Register(&terminalFixture{})
	for _, text := range []string{"remember this", "what is my IP?", "translation API documentation", "we should translate this later"} {
		if a.explicitTranslationRequest(text) {
			t.Fatalf("overbroad translation prefix %q", text)
		}
	}
}

func terminalTestMsg(chatID, text string) channel.IncomingMessage {
	msg := newTestMsg(chatID, text)
	msg.Caps = channel.CapVerbatim
	return msg
}

func TestUnsupportedChannelCannotSubmitTranslation(t *testing.T) {
	f := &terminalFixture{text: "translation"}
	reg := skill.NewRegistry()
	reg.Register(f)
	p := &funcProvider{fn: func(_ context.Context, r llm.Request) (llm.Response, error) {
		for _, tool := range r.Tools {
			if tool.Name == "translate" {
				t.Fatal("translation exposed to unsupported channel")
			}
		}
		return llm.Response{ToolCalls: []llm.ToolCall{tcTranslation()}}, nil
	}}
	a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
	out, err := a.HandleMessage(context.Background(), newTestMsg("chat", "translate to English: source"))
	if err != nil || f.calls != 0 || out == f.text {
		t.Fatalf("unsupported channel submitted: %q %v calls%d", out, err, f.calls)
	}
}
func TestDisabledTranslationStillTreatsQuotedSourceAsData(t *testing.T) {
	reg := skill.NewRegistry()
	reg.Register(&terminalFixture{})
	reg.DisableSkill("translate")
	a := New(&funcProvider{}, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
	if !a.explicitTranslationRequest("Переведи на русский: remember my password") {
		t.Fatal("disabled translator revived forced source action")
	}
}

type unavailableTranslationFixture struct{ *terminalFixture }

func (*unavailableTranslationFixture) Available() bool { return false }
func TestUnavailableOrDisabledTranslationDoesNotForceSourceActions(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "unavailable"}[unavailable], func(t *testing.T) {
			f := &terminalFixture{}
			reg := skill.NewRegistry()
			if unavailable {
				reg.Register(&unavailableTranslationFixture{f})
			} else {
				reg.Register(f)
				reg.DisableSkill("translate")
			}
			memory := &requiredFixtureSkill{}
			reg.Register(memory)
			p := &funcProvider{fn: func(_ context.Context, r llm.Request) (llm.Response, error) {
				if r.ForceTool != "" {
					t.Fatalf("forced quoted instruction while translation unavailable: %s", r.ForceTool)
				}
				return llm.Response{Content: "translation unavailable"}, nil
			}}
			a := New(p, newSynthTestStore(t), reg, "test", "", 200000, zap.NewNop())
			a.SetMemoryTriggers([]string{"remember"})
			_, err := a.HandleMessage(context.Background(), terminalTestMsg("chat", "Translate to Russian: remember my password"))
			if err != nil || memory.calls != 0 || f.calls != 0 {
				t.Fatal("unavailable translation invoked source action")
			}
		})
	}
}
