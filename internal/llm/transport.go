package llm

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// OfficialModelEndpoint validates a provider-bound HTTPS endpoint without
// contacting the provider. Credentials must never be forwarded to a user URL.
func OfficialModelEndpoint(provider, endpoint string) (string, error) {
	switch provider {
	case "deepseek":
		if endpoint == "" {
			endpoint = "https://api.deepseek.com/v1"
		}
	case "zai":
		if endpoint == "" {
			endpoint = "https://api.z.ai/api/paas/v4"
		}
	default:
		return "", fmt.Errorf("unsupported official provider")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.Port() != "" {
		return "", fmt.Errorf("untrusted model endpoint")
	}
	path := strings.TrimSuffix(u.Path, "/")
	valid := provider == "deepseek" && u.Host == "api.deepseek.com" && (path == "" || path == "/v1") || provider == "zai" && u.Host == "api.z.ai" && path == "/api/paas/v4"
	if !valid {
		return "", fmt.Errorf("untrusted model endpoint")
	}
	if provider == "deepseek" {
		u.Path = "/v1"
	} else {
		u.Path = "/api/paas/v4"
	}
	return u.String(), nil
}

// NewOfficialModelClient keeps the caller's proxy-aware transport but binds it
// to one official origin and disables redirects, including same-origin ones.
func NewOfficialModelClient(provider, endpoint string, base *http.Client) (*http.Client, string, error) {
	canonical, err := OfficialModelEndpoint(provider, endpoint)
	if err != nil {
		return nil, "", err
	}
	origin, _ := url.Parse(canonical)
	client := http.Client{}
	if base != nil {
		client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &boundModelTransport{inner: transport, origin: origin}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Model credentials never participate in a cookie jar inherited from a UI.
	client.Jar = nil
	return &client, canonical, nil
}

type boundModelTransport struct {
	inner  http.RoundTripper
	origin *url.URL
}

func (t *boundModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL
	if u == nil || u.Scheme != t.origin.Scheme || u.Host != t.origin.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || req.Host != "" && req.Host != t.origin.Host {
		return nil, fmt.Errorf("model request outside trusted endpoint")
	}
	allowedPath := u.Path == t.origin.Path+"/chat/completions" && req.Method == http.MethodPost || u.Path == t.origin.Path+"/models" && req.Method == http.MethodGet
	if !allowedPath {
		return nil, fmt.Errorf("model request outside trusted endpoint")
	}
	return t.inner.RoundTrip(req)
}
