package translation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/llm"
	"github.com/iulita-ai/iulita/internal/modelruntime"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeRuntime struct {
	mu          sync.Mutex
	key, status string
	deny        error
}

func (f *fakeRuntime) OperationAvailability(string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status == "" {
		return "ready"
	}
	return f.status
}
func (f *fakeRuntime) WithOperationConnection(ctx context.Context, p, o string, cb func(context.Context, modelruntime.Connection, uint64) error) error {
	f.mu.Lock()
	key, deny := f.key, f.deny
	f.mu.Unlock()
	if deny != nil {
		return deny
	}
	if key == "" {
		key = "synthetic"
	}
	return cb(ctx, modelruntime.Connection{Provider: p, APIKey: key, Endpoint: "https://api.z.ai/api/paas/v4", Enabled: true, Generation: "test"}, 7)
}
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const good = `{"agent_id":"general_translation","choices":[{"finish_reason":"stop","messages":[{"role":"assistant","content":{"type":"text","text":"  Привет\n{item} \u0060<x>\u0060  "}}]}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"total_calls":1}}`

func request() Request {
	return Request{Text: "Hello {item}", TargetLanguage: "ru", UserID: "trusted", ChatID: "chat"}
}
func errorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestTranslateWireAndAccounting(t *testing.T) {
	var attempts []llm.Attempt
	calls := 0
	s := New(&fakeRuntime{}, &http.Client{Timeout: time.Nanosecond, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != endpoint || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic" || r.Header.Get("Cookie") != "" {
			t.Fatalf("invalid transport request")
		}
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		if json.Unmarshal(data, &body) != nil {
			t.Fatal("invalid JSON")
		}
		if len(body) != 4 || body["agent_id"] != AgentID || body["stream"] != false {
			t.Fatalf("unexpected wire fields: %v", body)
		}
		vars := body["custom_variables"].(map[string]any)
		if vars["source_lang"] != "auto" || vars["target_lang"] != "ru" || vars["strategy"] != "general" {
			t.Fatal(vars)
		}
		if strings.Contains(string(data), "trusted") || strings.Contains(string(data), "chat") {
			t.Fatal("identity sent upstream")
		}
		messages := body["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		if content[0].(map[string]any)["text"] != request().Text {
			t.Fatal("source text changed")
		}
		return response(good), nil
	})}, func(_ context.Context, a llm.Attempt) { attempts = append(attempts, a) })
	out, err := s.Translate(context.Background(), request())
	if err != nil || out.Text != "  Привет\n{item} `<x>`  " {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if calls != 1 || len(attempts) != 1 {
		t.Fatalf("calls=%d attempts=%d", calls, len(attempts))
	}
	a := attempts[0]
	if a.Operation != "translation" || a.RequestedModel != AgentID || a.Model != AgentID || a.ModelVerified || a.PolicyRevision != 7 || a.ChargeUnknown || !a.UsageAvailable || a.Usage.InputTokens != 12 {
		t.Fatalf("wrong accounting: %+v", a)
	}
}

func TestNormalizeShapesAndFailures(t *testing.T) {
	object := strings.Replace(good, `"messages":[{`, `"messages":{`, 1)
	object = strings.Replace(object, `"}}]}]`, `"}}}]`, 1)
	for _, body := range []string{good, object, strings.Replace(good, `,"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16,"total_calls":1}`, "", 1)} {
		if out, err := normalize([]byte(body)); err != nil || out.Text == "" {
			t.Fatalf("valid fixture rejected: %s %v", body, err)
		}
	}
	for name, body := range map[string]string{
		"wrong agent": strings.Replace(good, AgentID, "other", 1), "incomplete": strings.Replace(good, `"stop"`, `"length"`, 1), "running": strings.Replace(good, `"agent_id"`, `"status":"running","agent_id"`, 1),
		"business error": strings.Replace(good, `"agent_id"`, `"error":{"message":"private source and key"},"agent_id"`, 1), "negative usage": strings.Replace(good, `"prompt_tokens":12`, `"prompt_tokens":-1`, 1), "usage total": strings.Replace(good, `"total_tokens":16`, `"total_tokens":99`, 1),
		"role": strings.Replace(good, `"assistant"`, `"user"`, 1), "unknown content": strings.Replace(good, `"type":"text"`, `"type":"image"`, 1), "empty choices": `{"agent_id":"general_translation","choices":[]}`, "malformed": `{`,
		"ambiguous": `{"agent_id":"general_translation","choices":[{"finish_reason":"stop","messages":[{"role":"assistant","content":{"type":"text","text":"one"}},{"role":"assistant","content":{"type":"text","text":"two"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := normalize([]byte(body))
			if err == nil || out.Text != "" {
				t.Fatalf("accepted invalid result: %+v %v", out, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("upstream text leaked")
			}
		})
	}
}

func TestValidationBeforeNetwork(t *testing.T) {
	for name, edit := range map[string]func(*Request){"empty identity": func(r *Request) { r.UserID = "" }, "empty text": func(r *Request) { r.Text = " \n" }, "bad UTF8": func(r *Request) { r.Text = string([]byte{255}) }, "rune bound": func(r *Request) { r.Text = strings.Repeat("a", MaxTextRunes+1) }, "byte bound": func(r *Request) { r.Text = strings.Repeat("😀", 17000) }, "unknown target": func(r *Request) { r.TargetLanguage = "AUTO" }, "target auto": func(r *Request) { r.TargetLanguage = "auto" }, "same language": func(r *Request) { r.SourceLanguage = "ru" }, "regional source unsupported": func(r *Request) { r.SourceLanguage = "en-US" }} {
		t.Run(name, func(t *testing.T) {
			var a llm.Attempt
			s := New(&fakeRuntime{}, &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network"); return nil, nil })}, func(_ context.Context, v llm.Attempt) { a = v })
			r := request()
			edit(&r)
			_, err := s.Translate(context.Background(), r)
			var e *Error
			if !errors.As(err, &e) || !e.NoCharge() || a.ChargeUnknown || a.Status != "rejected" {
				t.Fatalf("err=%v a=%+v", err, a)
			}
		})
	}
	r := request()
	r.SourceLanguage = "en"
	r.TargetLanguage = "en-US"
	if validate(&r) != nil {
		t.Fatal("regional variant incorrectly identical")
	}
}

func TestFailuresObservedOnceAndSanitized(t *testing.T) {
	for name, rt := range map[string]roundTrip{
		"timeout":        func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded },
		"upstream error": func(*http.Request) (*http.Response, error) { return nil, errors.New("SECRET SOURCE synthetic") },
		"oversize": func(*http.Request) (*http.Response, error) {
			return response(strings.Repeat("x", maxResponseBytes+1)), nil
		},
		"http auth": func(*http.Request) (*http.Response, error) {
			r := response(`{"error":{"message":"SECRET SOURCE"}}`)
			r.StatusCode = 401
			return r, nil
		},
		"redirect": func(*http.Request) (*http.Response, error) {
			r := response(`{}`)
			r.StatusCode = 307
			r.Header.Set("Location", "https://evil.test")
			return r, nil
		},
		"incomplete with usage": func(*http.Request) (*http.Response, error) {
			return response(strings.Replace(good, `"stop"`, `"length"`, 1)), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			n := 0
			var a llm.Attempt
			s := New(&fakeRuntime{}, &http.Client{Transport: rt}, func(_ context.Context, v llm.Attempt) { n++; a = v })
			out, err := s.Translate(context.Background(), request())
			if err == nil || out.Text != "" || n != 1 || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("err=%v n=%d", err, n)
			}
			if name == "incomplete with usage" {
				if !a.UsageAvailable || a.ChargeUnknown || a.Status != "incomplete" {
					t.Fatal(a)
				}
			} else if !a.ChargeUnknown {
				t.Fatal("unreported usage presented as free")
			}
		})
	}
}

func TestRotationAndRuntimeDenial(t *testing.T) {
	f := &fakeRuntime{key: "A"}
	var keys []string
	s := New(f, &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		keys = append(keys, r.Header.Get("Authorization"))
		return response(good), nil
	})}, nil)
	if _, err := s.Translate(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	f.key = "B"
	if _, err := s.Translate(context.Background(), request()); err != nil {
		t.Fatal(err)
	}
	f.deny = &modelruntime.Error{Code: "credential_revoked", Message: "SECRET"}
	_, err := s.Translate(context.Background(), request())
	if errorCode(err) != "credential_revoked" || len(keys) != 2 || keys[0] != "Bearer A" || keys[1] != "Bearer B" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}

func TestConcurrencyAndCancellationRelease(t *testing.T) {
	started := make(chan struct{}, 2)
	s := New(&fakeRuntime{}, &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { _, err := s.Translate(ctx, request()); done <- err }()
	<-started
	if _, err := s.Translate(ctx, request()); errorCode(err) != "busy" {
		t.Fatal(err)
	}
	r := request()
	r.UserID = "two"
	go func() { _, err := s.Translate(ctx, r); done <- err }()
	<-started
	r.UserID = "three"
	if _, err := s.Translate(ctx, r); errorCode(err) != "busy" {
		t.Fatal(err)
	}
	cancel()
	<-done
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != 0 || len(s.users) != 0 {
		t.Fatal("semaphore leaked")
	}
}

func TestAgentTransportBlocksOtherRecipients(t *testing.T) {
	called := false
	rt := agentTransport{roundTrip(func(*http.Request) (*http.Response, error) { called = true; return response(good), nil })}
	for _, u := range []string{"https://evil.test/api/v1/agents", "https://api.z.ai:443/api/v1/agents", "https://api.z.ai/api/v1/agents?", "https://api.z.ai/api/v1/agents?a=b", "https://api.z.ai/api/v1/%61gents", "https://user@api.z.ai/api/v1/agents", "http://api.z.ai/api/v1/agents", "https://api.z.ai/api/paas/v4/chat/completions"} {
		r, _ := http.NewRequest("POST", u, http.NoBody)
		if _, err := rt.RoundTrip(r); err == nil {
			t.Fatalf("allowed %s", u)
		}
	}
	r, _ := http.NewRequest("GET", endpoint, http.NoBody)
	if _, err := rt.RoundTrip(r); err == nil {
		t.Fatal("allowed GET")
	}
	r.Method = "POST"
	r.Host = "evil.test"
	if _, err := rt.RoundTrip(r); err == nil {
		t.Fatal("allowed Host")
	}
	if called {
		t.Fatal("untrusted request reached transport")
	}
}

func TestParentDeadlineAndObserverCleanup(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var observed llm.Attempt
	s := New(&fakeRuntime{}, &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("parent deadline extended")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}, func(ctx context.Context, a llm.Attempt) {
		if ctx.Err() != nil {
			t.Fatal("observer inherited cancelled context")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("observer cleanup unbounded")
		}
		observed = a
	})
	_, err := s.Translate(parent, request())
	if !errors.Is(err, context.DeadlineExceeded) || errorCode(err) != "timeout" || observed.Status != "cancelled" || !observed.ChargeUnknown {
		t.Fatalf("err=%v attempt=%+v", err, observed)
	}
}
