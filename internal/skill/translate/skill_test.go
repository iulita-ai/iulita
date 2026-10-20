package translate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/modelruntime"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/translation"
)

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type runtime struct{}

func (runtime) OperationAvailability(string) string { return "ready" }
func (runtime) WithOperationConnection(ctx context.Context, p, o string, cb func(context.Context, modelruntime.Connection, uint64) error) error {
	return cb(ctx, modelruntime.Connection{Provider: "zai", Enabled: true, APIKey: "synthetic"}, 1)
}

func TestNativeTerminalContract(t *testing.T) {
	calls := 0
	s := New(translation.New(runtime{}, &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"agent_id":"general_translation","choices":[{"finish_reason":"stop","messages":{"role":"assistant","content":{"type":"text","text":"Привет"}}}]}`))}, nil
	})}, nil))
	ctx := skill.WithUserID(context.Background(), "trusted-user")
	ctx = skill.WithChatID(ctx, "chat")
	result, err := s.ExecuteResult(ctx, json.RawMessage(`{"text":"Hello","target_language":"ru"}`))
	if err != nil || !result.Verbatim || result.Text != "Привет" || calls != 1 {
		t.Fatalf("%+v %v calls=%d", result, err, calls)
	}
	if !s.Available() || !s.TerminalResult() || !s.MainLoopOnly() || s.DefaultEnabled() || !s.SuppressResultPreview() || !s.SkipLedgerDedup() || !s.IsSensitiveOutput() {
		t.Fatal("invalid native contracts")
	}
	status := s.ServiceStatus()
	if status.Provider != "zai" || status.Service != translation.AgentID || status.Checked || status.State != "ready" {
		t.Fatal(status)
	}
}

func TestStrictInputAndTrustedIdentity(t *testing.T) {
	calls := 0
	s := New(translation.New(runtime{}, &http.Client{Transport: rt(func(*http.Request) (*http.Response, error) { calls++; t.Fatal("network called"); return nil, nil })}, nil))
	ctx := skill.WithUserID(context.Background(), "trusted")
	for _, in := range []string{`{"text":"secret","target_language":"ru","user_id":"fake"}`, `{"text":"secret","target_language":"ru"} {}`, `{"text":42,"target_language":"ru"}`, `null`, `[]`, `{"text":"secret","target_language":"auto"}`} {
		if _, err := s.ExecuteResult(ctx, json.RawMessage(in)); err == nil {
			t.Fatalf("accepted %s", in)
		}
	}
	if _, err := s.Execute(context.Background(), json.RawMessage(`{"text":"secret","target_language":"ru"}`)); err == nil {
		t.Fatal("missing trusted identity allowed")
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestManifestSchemaAndSafeSummary(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.SecretKeys) != 0 || len(m.ForceTriggers) != 0 || len(m.ConfigKeys) != 1 || m.ConfigKeys[0] != "skills.translate.enabled" {
		t.Fatalf("bad manifest: %+v", m)
	}
	s := New(nil)
	if s.Available() || s.ServiceStatus().State != "runtime_unavailable" {
		t.Fatal("nil service advertised ready")
	}
	var schema map[string]any
	if json.Unmarshal(s.InputSchema(), &schema) != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatal("schema")
	}
	props := schema["properties"].(map[string]any)
	if len(props) != 3 {
		t.Fatal("unexpected input property")
	}
	if got := s.SafeInputSummary(json.RawMessage(`{"text":"SECRET SOURCE","source_language":"en","target_language":"ru"}`)); got != "translation en→ru, chars=13" {
		t.Fatal(got)
	}
	if got := s.SafeInputSummary(json.RawMessage(`{"text":"SECRET SOURCE","source_language":"SECRET","target_language":"SECRET"}`)); strings.Contains(got, "SECRET") {
		t.Fatal("unsafe summary")
	}
	for _, v := range translation.TargetLanguages() {
		if v == "auto" || v == "pinyin" || v == "ipa" {
			t.Fatal("excluded target")
		}
	}
}
