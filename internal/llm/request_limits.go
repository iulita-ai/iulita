package llm

import (
	"context"
	"fmt"
	"net/http"
)

// OutputTokenLimit applies a request ceiling without enlarging a provider's
// configured limit. It performs no provider call and rejects invalid ceilings.
func (r Request) OutputTokenLimit(configured int64) (int64, error) {
	if r.MaxOutputTokens < 0 {
		return 0, fmt.Errorf("invalid output token limit")
	}
	if r.MaxOutputTokens > 0 && (configured <= 0 || r.MaxOutputTokens < configured) {
		return r.MaxOutputTokens, nil
	}
	return configured, nil
}

// RequestHTTPClient prevents implicit redirect replays for a single reserved
// invocation. The original client's transport, timeouts and policy stay intact.
func RequestHTTPClient(client *http.Client, req Request) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if !req.SingleAttempt && req.BeforeRequest == nil {
		return client
	}
	clone := *client
	if req.SingleAttempt {
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	if req.BeforeRequest != nil {
		base := client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		clone.Transport = requestAdmissionTransport{base: base, guard: req.BeforeRequest}
	}
	return &clone
}

type requestAdmissionTransport struct {
	base  http.RoundTripper
	guard func(context.Context) error
}

// RoundTrip revalidates local authority before the underlying HTTP admission.
func (t requestAdmissionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	if err := t.guard(request.Context()); err != nil {
		return nil, err
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}
