package httpfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type previewConfirmer struct {
	prompts   []Confirmation
	declineAt int
}

func (c *previewConfirmer) Confirm(_ context.Context, action Confirmation, expect Actor) (Decision, error) {
	c.prompts = append(c.prompts, action)
	return Decision{Approved: c.declineAt == 0 || len(c.prompts) != c.declineAt, Actor: expect, MaxBytes: action.MaxBytes}, nil
}

func TestMutationApprovalDescribesExactBodyAndQuery(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	conf := &previewConfirmer{}
	e := newTestEngine(t, srv.URL, true, conf)
	for _, amount := range []int{100, 10000} {
		plan := fmt.Sprintf(`{"version":1,"request":{"method":"POST","url":"http://payment.example.test/transfer?recipient=alice&amount=%d","body":{"type":"json","value":{"recipient":"alice","amount":%d}}}}`, amount, amount)
		if out := runPlan(t, e, plan); out.Outcome != OutcomeCompleted {
			t.Fatalf("mutation failed: %+v", out)
		}
	}
	if len(conf.prompts) != 2 || conf.prompts[0].Preview == conf.prompts[1].Preview {
		t.Fatalf("materially different effects got identical consent: %+v", conf.prompts)
	}
	for index, amount := range []int{100, 10000} {
		preview := conf.prompts[index].Preview
		for _, required := range []string{"payment.example.test/transfer", "recipient=alice", fmt.Sprintf("amount=%d", amount), fmt.Sprintf(`"amount": %d`, amount), `"recipient": "alice"`} {
			if !strings.Contains(preview, required) {
				t.Fatalf("approval omits %q: %s", required, preview)
			}
		}
	}
	if proxy.hits() != 2 {
		t.Fatalf("unexpected dispatch count %d", proxy.hits())
	}
}

func TestMutationApprovalRedactsSecretsAndPreservesIntent(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	conf := &previewConfirmer{}
	e := newTestEngine(t, srv.URL, true, conf)
	plan := `{"version":1,"request":{"method":"POST","url":"http://payment.example.test/transfer?%74oken=querySecretCanary&recipient=alice","auth":{"type":"bearer","token":"authSecretCanary"},"headers":[{"name":"X-Api-Key","values":["headerSecretCanary"]}],"body":{"type":"json","value":{"recipient":"alice","amount":123,"password":"bodySecretCanary","accessToken":"camelSecretCanary","nested":{"api-key":"nestedSecretCanary"},"ref":{"credential_ref":"nonexistent-secret-ref"}}}}}`
	// Deliberately fail before dispatch: the reference is unresolved. Its value may
	// never be resolved just to build a confirmation preview.
	out := runPlan(t, e, plan)
	if len(conf.prompts) < 2 {
		t.Fatalf("expected plaintext and mutation consent: %+v", out)
	}
	preview := conf.prompts[len(conf.prompts)-1].Preview
	for _, secret := range []string{"querySecretCanary", "authSecretCanary", "headerSecretCanary", "bodySecretCanary", "nestedSecretCanary", "camelSecretCanary", "nonexistent-secret-ref"} {
		if strings.Contains(preview, secret) {
			t.Fatalf("approval leaked %q: %s", secret, preview)
		}
	}
	for _, intent := range []string{"payment.example.test/transfer", "recipient=alice", `"amount": 123`, `"password": "[redacted]"`, `[redacted credential]`} {
		if !strings.Contains(preview, intent) {
			t.Fatalf("redaction hid material intent %q: %s", intent, preview)
		}
	}
	if proxy.hits() != 0 {
		t.Fatal("unresolved credential dispatched")
	}
}

func TestMutationChainConfirmsResolvedDestinationSeparately(t *testing.T) {
	proxy := newFakeProxy(cannedResponse{status: 201, headers: map[string]string{"Location": "http://other.example.test/transfer?recipient=mallory"}, body: `{}`})
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	conf := &previewConfirmer{declineAt: 2}
	e := newTestEngine(t, srv.URL, true, conf)
	plan := `{"version":1,"steps":[{"id":"create","method":"POST","url":"http://first.example.test/create"},{"id":"next","method":"POST","url_from":{"step":"create","header":"Location","index":0},"body":{"type":"json","value":{"amount":10000}}}]}`
	out := runPlan(t, e, plan)
	if out.Outcome != OutcomeDeclined || len(conf.prompts) != 2 {
		t.Fatalf("chain used blanket grant: %+v prompts=%+v", out, conf.prompts)
	}
	if strings.Contains(conf.prompts[0].Preview, "other.example.test") || !strings.Contains(conf.prompts[1].Preview, "http://other.example.test/transfer?recipient=mallory") || !strings.Contains(conf.prompts[1].Preview, `"amount": 10000`) {
		t.Fatalf("unknown target approved blindly: %+v", conf.prompts)
	}
	if proxy.hits() != 1 {
		t.Fatalf("declined dynamic mutation dispatched: %d", proxy.hits())
	}
}

func TestMutationApprovalTooLargeFailsBeforeDispatch(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	conf := &previewConfirmer{}
	e := newTestEngine(t, srv.URL, true, conf)
	plan := fmt.Sprintf(`{"version":1,"request":{"method":"POST","url":"http://example.test/write","body":{"type":"text","value":%q}}}`, strings.Repeat("A", 4000))
	out := runPlan(t, e, plan)
	if out.Outcome != OutcomePolicyDenied || out.Steps[0].Error.Code != "confirmation_preview" || len(conf.prompts) != 0 || proxy.hits() != 0 {
		t.Fatalf("unreviewable mutation was truncated or dispatched: %+v", out)
	}
}

func TestMutationPreviewMasksOnlyReferencedSensitiveVariables(t *testing.T) {
	target, err := url.Parse("http://payment.example.test/transfer?recipient=alice;amount=10000")
	if err != nil {
		t.Fatal(err)
	}
	state := &runState{vars: map[string]varValue{"unused": {Value: "payment.example.test", Sensitive: true}, "credential": {Value: "variableSecretCanary", Sensitive: true}}}
	step := Request{Method: "POST", Body: &BodySpec{Type: BodyJSON, RawJSON: json.RawMessage(`{"recipient":"alice","lookup":{"$var":"credential"},"amount":9007199254740993}`)}}
	preview, err := mutationPreview(step, target, nil, state)
	if err != nil || !strings.Contains(preview, "payment.example.test/transfer?recipient=alice;amount=10000") || strings.Contains(preview, "variableSecretCanary") || !strings.Contains(preview, "9007199254740993") {
		t.Fatalf("redaction lost origin/precision or leaked variable: %s, %v", preview, err)
	}
}

func TestMutationPreviewRejectsEntirelyOpaqueSensitiveTargetOrText(t *testing.T) {
	proxy := newFakeProxy()
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()
	conf := &previewConfirmer{}
	e := newTestEngine(t, srv.URL, true, conf)
	for _, plan := range []string{
		`{"version":1,"request":{"method":"POST","url":"http://example.test/update","body":{"type":"text","value":"password=bodySecretCanary;amount=10000"}}}`,
		`{"version":1,"request":{"method":"POST","url":"http://example.test/token=targetSecretCanary"}}`,
	} {
		out := runPlan(t, e, plan)
		if out.Outcome != OutcomePolicyDenied || proxy.hits() != 0 || len(conf.prompts) != 0 {
			t.Fatalf("opaque sensitive intent was approved blindly: %+v", out)
		}
	}
}
