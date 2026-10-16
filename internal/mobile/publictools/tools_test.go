package publictools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/mobile/conversations"
	"github.com/iulita-ai/iulita/internal/skill"
	"github.com/iulita-ai/iulita/internal/skill/weather"
	"github.com/iulita-ai/iulita/internal/skill/webfetch"
	"github.com/iulita-ai/iulita/internal/skill/websearch"
	"github.com/iulita-ai/iulita/internal/web"
)

type lookup map[string]skill.Skill

func (l lookup) Get(name string) (skill.Skill, bool) { s, ok := l[name]; return s, ok }

type fakeSkill struct {
	name   string
	calls  int
	output string
	err    error
}

func (s *fakeSkill) Name() string                 { return s.name }
func (s *fakeSkill) Description() string          { return s.name }
func (s *fakeSkill) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *fakeSkill) Execute(context.Context, json.RawMessage) (string, error) {
	s.calls++
	return s.output, s.err
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeSearcher struct {
	calls int
	err   error
}

func (s *fakeSearcher) Search(context.Context, string, int) ([]web.SearchResult, error) {
	s.calls++
	return []web.SearchResult{{Title: "public result"}}, s.err
}

func TestAllowlistDisablementValidationAndRedaction(t *testing.T) {
	search := &fakeSearcher{}
	registry := lookup{"websearch": websearch.New(search, nil), "memory": &fakeSkill{name: "memory"}, "shell_exec": &fakeSkill{name: "shell_exec"}}
	tools := Tools(registry)
	if len(tools) != 1 || tools[0].Definition.Name != "websearch" || !tools[0].Isolated || tools[0].Effect != "read" || len(tools[0].RequiredScopes) != 1 || tools[0].RequiredScopes[0] != "chats:write" {
		t.Fatal("unsafe capability bridge", tools)
	}
	execute := tools[0].Execute
	for _, raw := range []string{`null`, ` `, `{"query":"ok","credential":"secret"}`, `{"query":123}`, `{"query":"ok"} {}`, `{"query":""}`} {
		out, err := execute(context.Background(), conversations.Fence{}, json.RawMessage(raw))
		if err != nil || !strings.Contains(out, "invalid_input") || search.calls != 0 {
			t.Fatal("invalid input dispatched", raw, err)
		}
	}
	search.err = errors.New("https://proxy.example/?secret=canary-secret")
	out, err := execute(context.Background(), conversations.Fence{}, json.RawMessage(`{"query":"forecast"}`))
	if err != nil || !strings.Contains(out, "lookup_failed") || strings.Contains(out, "canary") || search.calls != 1 {
		t.Fatal("read error not safely reported", out, err)
	}
	delete(registry, "websearch")
	if _, err = execute(context.Background(), conversations.Fence{}, json.RawMessage(`{"query":"again"}`)); err == nil || search.calls != 1 {
		t.Fatal("disabled tool dispatched")
	}
	registry["websearch"] = &fakeSkill{name: "websearch"}
	if len(Tools(registry)) != 0 {
		t.Fatal("name collision exposed a non-builtin skill")
	}
	if _, err = execute(context.Background(), conversations.Fence{}, json.RawMessage(`{"query":"again"}`)); err == nil || search.calls != 1 {
		t.Fatal("replacement inherited a pinned tool policy")
	}
}

func TestWeatherNeedsExplicitLocationAndUsesConfiguredHTTPClient(t *testing.T) {
	var requests int
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		var body string
		switch r.URL.Host {
		case "geocoding-api.open-meteo.com":
			if r.URL.Query().Get("name") != "Paris" {
				t.Fatal("wrong city", r.URL.Query())
			}
			body = `{"results":[{"name":"Paris","country":"France","latitude":48.85,"longitude":2.35}]}`
		case "api.open-meteo.com":
			if r.URL.Query().Get("forecast_days") != "2" {
				t.Fatal("tomorrow omitted")
			}
			body = `{"daily":{"time":["2026-10-16","2026-10-17"],"weather_code":[0,3],"temperature_2m_max":[18,19],"temperature_2m_min":[10,11],"precipitation_sum":[0,1],"wind_speed_10m_max":[5,6],"uv_index_max":[2,2]}}`
		default:
			t.Fatal("unexpected outbound destination", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	// Nil storage/registry intentionally panic if legacy memory/IP resolution
	// is accidentally reintroduced into mobile weather.
	s := weather.New(nil, nil, client)
	tool := Tools(lookup{"weather": s})[0]
	for _, raw := range []string{`{}`, `{"location":" "}`, `{"location":"Paris","days":17}`} {
		if _, err := tool.Execute(context.Background(), conversations.Fence{}, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 0 {
		t.Fatal("missing location used server IP or a memory default")
	}
	out, err := tool.Execute(context.Background(), conversations.Fence{}, json.RawMessage(`{"location":"Paris","days":2}`))
	if err != nil || requests != 2 || !strings.Contains(out, "Paris") || !strings.Contains(out, "Oct 17") {
		t.Fatal("no actual tomorrow forecast", out, err, requests)
	}
}

func TestWebFetchPreservesSSRFGuardAndDoesNotCallPrivateHTTP(t *testing.T) {
	var requests int
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { requests++; return nil, errors.New("must not dispatch") })}
	tool := Tools(lookup{"webfetch": webfetch.New(client)})[0]
	for _, url := range []string{"http://127.0.0.1/secret", "http://169.254.169.254/latest/meta-data/", "http://[::1]/", "file:///etc/passwd"} {
		body, _ := json.Marshal(map[string]string{"url": url})
		out, err := tool.Execute(context.Background(), conversations.Fence{}, body)
		if err != nil || !strings.Contains(out, "lookup_failed") {
			t.Fatal("private fetch result", out, err)
		}
	}
	if requests != 0 {
		t.Fatal("private URL reached transport")
	}
}

func TestWeatherProviderErrorsNeverReturnAPIKeysToTheModel(t *testing.T) {
	client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider failed with canary-private-api-key")
	})}
	s := weather.New(nil, nil, client)
	s.OnConfigChanged("skills.weather.openweathermap_api_key", "canary-private-api-key")
	tool := Tools(lookup{"weather": s})[0]
	out, err := tool.Execute(context.Background(), conversations.Fence{}, json.RawMessage(`{"location":"Paris","days":2}`))
	if err != nil || !strings.Contains(out, "lookup_failed") || strings.Contains(out, "canary") {
		t.Fatal("weather error disclosed transport credentials", out, err)
	}
}
