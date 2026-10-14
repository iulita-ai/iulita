package httpfetch

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// fakeResolver resolves every host to a stable public IP so SSRF preflight
// passes while the fake proxy handles the actual traffic.
type fakeResolver struct{}

func (fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

// proxiedRequest is one request observed by the fake forward proxy.
type proxiedRequest struct {
	Method string
	URI    string
	Header http.Header
	Body   string
}

// fakeProxy is an HTTP forward proxy that records requests and replies with
// canned responses. It doubles as the origin responder.
type fakeProxy struct {
	mu        sync.Mutex
	requests  []proxiedRequest
	responses []cannedResponse
	hit       atomic.Int64
}

type cannedResponse struct {
	status  int
	headers map[string]string
	body    string
}

func newFakeProxy(responses ...cannedResponse) *fakeProxy {
	return &fakeProxy{responses: responses}
}

func (p *fakeProxy) handler(w http.ResponseWriter, r *http.Request) {
	p.hit.Add(1)
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.requests = append(p.requests, proxiedRequest{Method: r.Method, URI: r.RequestURI, Header: r.Header.Clone(), Body: string(body)})
	resp := cannedResponse{status: 200, headers: map[string]string{"Content-Type": "application/json"}, body: `{"ok":true}`}
	if len(p.responses) > 0 {
		resp = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	for k, v := range resp.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write([]byte(resp.body))
}

func (p *fakeProxy) observed() []proxiedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]proxiedRequest, len(p.requests))
	copy(out, p.requests)
	return out
}

func (p *fakeProxy) hits() int64 { return p.hit.Load() }

// approvingConfirmer approves every consent with the requesting actor.
type approvingConfirmer struct {
	asked []string
}

func (c *approvingConfirmer) Confirm(_ context.Context, action Confirmation, expect Actor) (Decision, error) {
	c.asked = append(c.asked, action.Reason)
	return Decision{Approved: true, Actor: expect, MaxBytes: action.MaxBytes}, nil
}

// wrongActorConfirmer approves but returns a different actor identity — the
// engine must reject the decision.
type wrongActorConfirmer struct{}

func (wrongActorConfirmer) Confirm(_ context.Context, _ Confirmation, _ Actor) (Decision, error) {
	return Decision{Approved: true, Actor: Actor{UserID: "someone-else", Channel: "test"}}, nil
}

// ctxConfirmer injects a per-context asker (channel-bound style).
type ctxConfirmer struct {
	inner *approvingConfirmer
}

func (c *ctxConfirmer) Confirm(ctx context.Context, action Confirmation, expect Actor) (Decision, error) {
	return c.inner.Confirm(ctx, action, expect)
}

type decliningConfirmer struct{}

func (decliningConfirmer) Confirm(_ context.Context, _ Confirmation, expect Actor) (Decision, error) {
	return Decision{Approved: false, Actor: expect}, nil
}

// newTestEngine builds an engine wired to the given proxy server.
func newTestEngine(t *testing.T, proxyURL string, enabled bool, confirmer Confirmer) *Engine {
	t.Helper()
	e, perr := NewEngine(EngineConfig{
		ProxyURL:  proxyURL,
		Limits:    testLimits(),
		Policy:    Policy{Enabled: enabled},
		Resolver:  fakeResolver{},
		Confirmer: confirmer,
	})
	if perr != nil {
		t.Fatalf("engine: %v", perr)
	}
	return e
}

func testLimits() Limits {
	l := DefaultLimits()
	l.WallTimeout = 20 * time.Second
	l.RequestTimeout = 10 * time.Second
	l.ChainBudget = 15 * time.Second
	return l
}

func testScope() Scope {
	return Scope{UserID: "u1", ChatID: "c1", Mode: ModeInteractive, Caller: Caller{ID: "test", Kind: CallerTool}}
}

func runPlan(t *testing.T, e *Engine, planJSON string) RunResult {
	t.Helper()
	plan, err := ParsePlan([]byte(planJSON))
	if err != nil {
		// Mirrors the adapter: parse rejections surface as validation_error.
		return RunResult{
			Version: 1, OperationID: "test", Outcome: OutcomeValidationError,
			Error: &ErrorInfo{Outcome: OutcomeValidationError, Code: "validation", Message: err.Error()},
		}
	}
	res, err := e.Run(WithScope(context.Background(), testScope()), plan)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

const getPlan = `{
	"version": 1,
	"request": {
		"id": "items", "method": "GET", "url": "http://api.example.test/v1/items",
		"headers": [{"name": "Accept", "values": ["application/json"]}],
		"response": {"mode": "inline"}
	}
}`

func TestEngineGETThroughProxy(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	e := newTestEngine(t, srv.URL, true, nil)
	res := runPlan(t, e, getPlan)

	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, want completed (err: %+v)", res.Outcome, res.Error)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("steps = %d", len(res.Steps))
	}
	st := res.Steps[0]
	if st.Status != 200 || st.Body == nil || st.Body.Text != `{"ok":true}` {
		t.Fatalf("step = %+v", st)
	}
	if !st.ProxyUsed {
		t.Fatal("proxy_used = false")
	}
	if st.BytesRead != int64(len(`{"ok":true}`)) || !st.BodyComplete {
		t.Fatalf("counters: read=%d complete=%v", st.BytesRead, st.BodyComplete)
	}
	obs := proxy.observed()
	if len(obs) != 1 {
		t.Fatalf("proxy observed %d requests, want 1", len(obs))
	}
	if obs[0].URI != "http://api.example.test/v1/items" {
		t.Fatalf("proxy URI = %s (absolute-form expected for http origins)", obs[0].URI)
	}
	if obs[0].Header.Get("Accept") != "application/json" {
		t.Fatalf("Accept header missing at proxy: %+v", obs[0].Header)
	}
}

func TestEnginePOSTRequiresConsent(t *testing.T) {
	postPlan := `{
		"version": 1,
		"request": {
			"id": "create", "method": "POST", "url": "http://api.example.test/v1/items",
			"body": {"type": "json", "value": {"name": "demo"}},
			"response": {"mode": "inline"}
		}
	}`
	// Fresh proxy per subtest: hit counters must not leak between cases.
	newCase := func(t *testing.T) (*fakeProxy, *Engine, Confirmer) {
		t.Helper()
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		t.Cleanup(srv.Close)
		return proxy, nil, nil
	}
	_ = newCase

	t.Run("no confirmer fails closed", func(t *testing.T) {
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		defer srv.Close()
		e := newTestEngine(t, srv.URL, true, nil)
		res := runPlan(t, e, postPlan)
		if res.Outcome != OutcomeConfirmationRequired {
			t.Fatalf("outcome = %s, want confirmation_required", res.Outcome)
		}
		if proxy.hits() != 0 {
			t.Fatalf("proxy hit %d times without consent", proxy.hits())
		}
	})

	t.Run("declined stays declined", func(t *testing.T) {
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		defer srv.Close()
		e := newTestEngine(t, srv.URL, true, decliningConfirmer{})
		res := runPlan(t, e, postPlan)
		if res.Outcome != OutcomeDeclined {
			t.Fatalf("outcome = %s, want declined", res.Outcome)
		}
		if proxy.hits() != 0 {
			t.Fatalf("proxy hit after decline")
		}
	})

	t.Run("approved POST reaches proxy with body", func(t *testing.T) {
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		defer srv.Close()
		conf := &approvingConfirmer{}
		e := newTestEngine(t, srv.URL, true, conf)
		res := runPlan(t, e, postPlan)
		if res.Outcome != OutcomeCompleted {
			t.Fatalf("outcome = %s, want completed (err %+v)", res.Outcome, res.Error)
		}
		if len(conf.asked) != 1 || conf.asked[0] != string(ConfirmMutation) {
			t.Fatalf("consent reasons = %v", conf.asked)
		}
		obs := proxy.observed()
		if len(obs) == 0 || obs[len(obs)-1].Method != "POST" {
			t.Fatalf("no POST observed: %+v", proxy.observed())
		}
		last := obs[len(obs)-1]
		if !strings.Contains(last.Body, `"name":"demo"`) {
			t.Fatalf("POST body = %q", last.Body)
		}
		if ct := last.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want engine-set application/json", ct)
		}
	})

	t.Run("actor mismatch rejected", func(t *testing.T) {
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		defer srv.Close()
		e := newTestEngine(t, srv.URL, true, wrongActorConfirmer{})
		res := runPlan(t, e, postPlan)
		if res.Outcome != OutcomeDeclined {
			t.Fatalf("outcome = %s, want declined (actor mismatch)", res.Outcome)
		}
		if proxy.hits() != 0 {
			t.Fatal("request executed with mismatched actor")
		}
	})

	t.Run("context confirmer takes precedence", func(t *testing.T) {
		proxy := newFakeProxy()
		srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
		defer srv.Close()
		conf := &approvingConfirmer{}
		e := newTestEngine(t, srv.URL, true, nil) // no engine-level confirmer
		ctx := interact.WithConfirmer(WithScope(context.Background(), testScope()), &ctxConfirmer{inner: conf})
		plan, err := ParsePlan([]byte(postPlan))
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.Run(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		if res.Outcome != OutcomeCompleted {
			t.Fatalf("outcome = %s, want completed via ctx confirmer (%+v)", res.Outcome, res.Error)
		}
		if proxy.hits() != 1 {
			t.Fatalf("proxy hits = %d, want 1", proxy.hits())
		}
	})
}

func TestEngineHTTPErrorBodiesPreserved(t *testing.T) {
	cases := []struct {
		status int
		body   string
	}{
		{401, `{"error":"unauthorized"}`},
		{422, `{"error":"validation"}`},
		{429, `{"error":"rate_limited"}`},
		{500, `{"error":"boom"}`},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			proxy := newFakeProxy(cannedResponse{
				status:  tc.status,
				headers: map[string]string{"Content-Type": "application/json", "WWW-Authenticate": `Basic realm="api"`},
				body:    tc.body,
			})
			srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
			defer srv.Close()
			e := newTestEngine(t, srv.URL, true, nil)
			res := runPlan(t, e, getPlan)
			if res.Outcome != OutcomeHTTPError {
				t.Fatalf("outcome = %s, want http_error", res.Outcome)
			}
			st := res.Steps[0]
			if st.Status != tc.status || st.Body == nil || st.Body.Text != tc.body {
				t.Fatalf("step = %+v", st)
			}
			// Status and body must coexist — the error never erases the body.
			var sawWWW bool
			for _, h := range st.Headers {
				if h.Name == "Www-Authenticate" {
					sawWWW = true
				}
			}
			if !sawWWW {
				t.Fatalf("WWW-Authenticate header missing: %+v", st.Headers)
			}
		})
	}
}

func TestProxyFailureNeverDirect(t *testing.T) {
	// A closed proxy listener: every dial fails. The origin name resolves
	// publicly only via the fake resolver; any direct fallback would need a
	// real network route to api.example.test and would still be observable
	// as a non-transport_error outcome.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadURL := "http://" + dead.Addr().String()
	_ = dead.Close()

	// NO_PROXY must not create an escape hatch.
	t.Setenv("NO_PROXY", "*")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")

	e := newTestEngine(t, deadURL, true, nil)
	res := runPlan(t, e, getPlan)
	if res.Outcome != OutcomeTransportError {
		t.Fatalf("outcome = %s (%+v), want transport_error — never a direct-connection success", res.Outcome, res.Error)
	}
	outJSON, _ := res.JSON()
	if strings.Contains(outJSON, "93.184.216.34") {
		t.Fatalf("resolved IP leaked into result: %s", outJSON)
	}
}

func TestEngineDisabled(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, false, nil)
	res := runPlan(t, e, getPlan)
	if res.Outcome != OutcomePolicyDenied {
		t.Fatalf("outcome = %s, want policy_denied", res.Outcome)
	}
	if proxy.hits() != 0 {
		t.Fatal("disabled engine performed a request")
	}
}

func TestEngineBasicAuthHeader(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, &approvingConfirmer{})

	plan := `{
		"version": 1,
		"request": {
			"id": "auth", "method": "GET", "url": "http://api.example.test/v1/me",
			"auth": {"type": "basic", "username": "alice", "password": "s3cret"},
			"response": {"mode": "inline"}
		}
	}`
	res := runPlan(t, e, plan)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s (%+v)", res.Outcome, res.Error)
	}
	obs := proxy.observed()
	if len(obs) == 0 {
		t.Fatal("no request observed")
	}
	if got := obs[0].Header.Get("Authorization"); got != "Basic "+basicB64("alice", "s3cret") {
		t.Fatalf("Authorization = %q", got)
	}
	// The password must never appear in the result envelope.
	outJSON, _ := res.JSON()
	if strings.Contains(outJSON, "s3cret") {
		t.Fatalf("secret leaked into result: %s", outJSON)
	}
}

func basicB64(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", user, pass)))
}

func TestSensitiveResponseHeadersMasked(t *testing.T) {
	proxy := newFakeProxy(cannedResponse{
		status: 200,
		headers: map[string]string{
			"Content-Type": "application/json",
			"Set-Cookie":   "session=topsecret; Path=/; HttpOnly",
		},
		body: `{}`,
	})
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, nil)
	res := runPlan(t, e, getPlan)
	outJSON, _ := res.JSON()
	if strings.Contains(outJSON, "topsecret") {
		t.Fatalf("Set-Cookie value leaked: %s", outJSON)
	}
	if !strings.Contains(outJSON, "[redacted]") {
		t.Fatalf("expected redaction marker: %s", outJSON)
	}
}

func TestRedirectNoneReturnsLocation(t *testing.T) {
	proxy := newFakeProxy(cannedResponse{
		status:  302,
		headers: map[string]string{"Location": "/v2/items"},
		body:    "",
	})
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, nil)
	res := runPlan(t, e, getPlan)
	if res.Outcome != OutcomeHTTPError {
		t.Fatalf("outcome = %s (3xx outside default expect)", res.Outcome)
	}
	st := res.Steps[0]
	if st.Status != 302 || len(st.Redirects) != 1 || st.Redirects[0].Location != "/v2/items" {
		t.Fatalf("step = %+v", st)
	}
}

func TestChainExtractAndUse(t *testing.T) {
	proxy := newFakeProxy(
		cannedResponse{
			status:  201,
			headers: map[string]string{"Content-Type": "application/json"},
			body:    `{"id": "it_42", "name": "demo"}`,
		},
		cannedResponse{
			status:  200,
			headers: map[string]string{"Content-Type": "application/json"},
			body:    `{"got": "it_42"}`,
		},
	)
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, &approvingConfirmer{})

	plan := `{
		"version": 1, "cookie_mode": "chain",
		"steps": [
			{
				"id": "create", "method": "POST", "url": "http://api.example.test/v1/items",
				"body": {"type": "json", "value": {"name": "demo"}},
				"extract": [{"name": "item_id", "source": "json_pointer", "pointer": "/id", "type": "string"}],
				"expect_status": [201],
				"response": {"mode": "inline"}
			},
			{
				"id": "inspect", "method": "GET", "url": "http://api.example.test/v1/",
				"path_segments": ["items", {"$var": "item_id"}],
				"response": {"mode": "inline"}
			}
		]
	}`
	res := runPlan(t, e, plan)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s, stop=%s (err %+v)", res.Outcome, res.StopReason, res.Error)
	}
	obs := proxy.observed()
	if len(obs) != 2 {
		t.Fatalf("observed %d requests, want 2", len(obs))
	}
	if !strings.HasSuffix(obs[1].URI, "/v1/items/it_42") {
		t.Fatalf("second request URI = %s", obs[1].URI)
	}
	// The extracted id must not be echoed as a variable value anywhere.
	outJSON, _ := res.JSON()
	if strings.Contains(outJSON, `"item_id"`) && strings.Contains(outJSON, "it_42\":") {
		t.Fatalf("variable value leaked: %s", outJSON)
	}
}

func TestStopOnUnexpectedStatus(t *testing.T) {
	proxy := newFakeProxy(cannedResponse{
		status:  500,
		headers: map[string]string{"Content-Type": "application/json"},
		body:    `{"e":1}`,
	})
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, &approvingConfirmer{})

	plan := `{
		"version": 1,
		"steps": [
			{"id": "a", "method": "GET", "url": "http://api.example.test/a", "response": {"mode": "inline"}},
			{"id": "b", "method": "GET", "url": "http://api.example.test/b", "response": {"mode": "inline"}}
		]
	}`
	res := runPlan(t, e, plan)
	if res.Outcome != OutcomeHTTPError {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("chain continued after 500: %d steps", len(res.Steps))
	}
	if res.StopReason == "" {
		t.Fatal("stop reason missing")
	}
}

func TestValidationBeforeNetwork(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, nil)

	cases := []struct {
		name    string
		plan    string
		wantSub string
	}{
		{"unknown field", `{"version":1,"request":{"url":"http://api.example.test/"},"bogus":1}`, "bogus"},
		{"bad enum", `{"version":1,"request":{"url":"http://api.example.test/"},"cookie_mode":"always"}`, "cookie_mode"},
		{"both url sources", `{"version":1,"request":{"url":"http://api.example.test/","url_from":{"step":"x","header":"Location"}}}`, "url"},
		{"forward var ref", `{"version":1,"steps":[{"id":"a","method":"GET","url":"http://api.example.test/","path_segments":[{"$var":"later"}]},{"id":"b","method":"GET","url":"http://api.example.test/","extract":[{"name":"later","source":"status"}]}]}`, "forward"},
		{"secret in path", `{"version":1,"request":{"url":"http://api.example.test/","path_segments":[{"credential_ref":"k"}]}}`, "path"},
		{"userinfo url", `{"version":1,"request":{"url":"http://u:p@api.example.test/"}}`, "userinfo"},
		{"fragment url", `{"version":1,"request":{"url":"http://api.example.test/#x"}}`, "fragment"},
		{"auth conflict", `{"version":1,"request":{"url":"http://api.example.test/","auth":{"type":"bearer","token":"t"},"headers":[{"name":"Authorization","values":["x"]}]}}`, "Authorization"},
		{"hop-by-hop header", `{"version":1,"request":{"url":"http://api.example.test/","headers":[{"name":"Connection","values":["close"]}]}}`, "transport-managed"},
		{"content-type conflict", `{"version":1,"request":{"method":"POST","url":"http://api.example.test/","body":{"type":"json","value":{}},"headers":[{"name":"Content-Type","values":["text/plain"]}]}}`, "conflicts"},
		{"auth union mixed", `{"version":1,"request":{"url":"http://api.example.test/","auth":{"type":"basic","credential_ref":"k","username":"u","password":"p"}}}`, "either"},
		{"bad body type", `{"version":1,"request":{"method":"POST","url":"http://api.example.test/","body":{"type":"xml","value":"<a/>"}}}`, "body type"},
		{"HEAD with body", `{"version":1,"request":{"method":"HEAD","url":"http://api.example.test/","body":{"type":"text","value":"x"}}}`, "HEAD"},
		{"segments need slash", `{"version":1,"request":{"url":"http://api.example.test/base","path_segments":["x"]}}`, "must end with '/'"},
		{"url query duplication", `{"version":1,"request":{"url":"http://api.example.test/?a=1","query":[{"name":"b","value":"2"}]}}`, "one source"},
		{"expect status range", `{"version":1,"request":{"url":"http://api.example.test/","expect_status":[{"from":500,"to":200}]}}`, "invalid status range"},
		{"mixed json ref object", `{"version":1,"request":{"method":"POST","url":"http://api.example.test/","body":{"type":"json","value":{"$var":"x","extra":1}}}}`, "exactly one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runPlan(t, e, tc.plan)
			if res.Outcome != OutcomeValidationError {
				t.Fatalf("outcome = %s, want validation_error (%+v)", res.Outcome, res.Error)
			}
			if !strings.Contains(res.Error.Message, tc.wantSub) {
				t.Fatalf("message %q does not contain %q", res.Error.Message, tc.wantSub)
			}
		})
	}
	if proxy.hits() != 0 {
		t.Fatalf("validation failures reached the network: %d hits", proxy.hits())
	}
}

func TestSSRFPrivateTargetsDenied(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	// Engine whose resolver returns a PRIVATE address — preflight must deny.
	privResolver := privResolverType{}
	e, perr := NewEngine(EngineConfig{
		ProxyURL: srv.URL,
		Limits:   testLimits(),
		Policy:   Policy{Enabled: true},
		Resolver: privResolver,
	})
	if perr != nil {
		t.Fatal(perr)
	}
	res := runPlan(t, e, `{"version":1,"request":{"url":"http://api.example.test/x"}}`)
	if res.Outcome != OutcomePolicyDenied {
		t.Fatalf("outcome = %s, want policy_denied (SSRF)", res.Outcome)
	}
	if proxy.hits() != 0 {
		t.Fatal("SSRF-denied target reached the proxy")
	}

	// Literal private IP in the URL: denied regardless of resolver.
	e2 := newTestEngine(t, srv.URL, true, nil)
	res2 := runPlan(t, e2, `{"version":1,"request":{"url":"http://127.0.0.1:8080/x"}}`)
	if res2.Outcome != OutcomePolicyDenied {
		t.Fatalf("literal IP outcome = %s, want policy_denied", res2.Outcome)
	}
	res3 := runPlan(t, e2, `{"version":1,"request":{"url":"http://[::ffff:10.0.0.5]/x"}}`)
	if res3.Outcome != OutcomePolicyDenied {
		t.Fatalf("mapped v6 outcome = %s, want policy_denied", res3.Outcome)
	}
}

type privResolverType struct{}

func (privResolverType) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
}

func TestDiscardModeNoBodyRead(t *testing.T) {
	bigBody := strings.Repeat("x", 1<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()
	e := newTestEngine(t, srv.URL, true, nil)
	res := runPlan(t, e, `{"version":1,"request":{"url":"http://api.example.test/big","response":{"mode":"discard"}}}`)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	st := res.Steps[0]
	if st.Body != nil {
		t.Fatal("discard produced a body view")
	}
	if st.BytesRead != 0 {
		t.Fatalf("discard read %d bytes", st.BytesRead)
	}
}
