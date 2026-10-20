package translation

import (
	"fmt"
	"net/http"
)

const endpoint = "https://api.z.ai/api/v1/agents"

// The transport is operation-specific: chat clients retain their own allowlist.
type agentTransport struct{ inner http.RoundTripper }

// RoundTrip rejects every recipient, method and path outside the agent operation.
func (t agentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := r.URL
	if u == nil || u.Scheme != "https" || u.Host != "api.z.ai" || u.Path != "/api/v1/agents" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.User != nil || r.Method != http.MethodPost || (r.Host != "" && r.Host != "api.z.ai") {
		return nil, fmt.Errorf("translation endpoint rejected")
	}
	return t.inner.RoundTrip(r)
}
func newClient(base *http.Client) *http.Client {
	rt := http.DefaultTransport
	if base != nil && base.Transport != nil {
		rt = base.Transport
	}
	return &http.Client{Transport: agentTransport{rt}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
