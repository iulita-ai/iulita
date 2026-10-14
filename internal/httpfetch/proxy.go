package httpfetch

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// ProxyError is a structured proxy configuration failure. Proxy problems are
// never downgraded to direct connections (plan §7.1).
type ProxyError struct {
	Code    string // proxy_required | proxy_invalid
	Message string
}

func (e *ProxyError) Error() string { return e.Code + ": " + e.Message }

// supportedProxySchemes are the proxy schemes validated at construction.
// socks5 and socks5h behave identically in Go (DNS resolves at the proxy for
// both); docs state this instead of promising curl CLI equivalence.
var supportedProxySchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

// NewProxyOnlyTransport builds the strict transport for the HTTP engine:
//
//   - the proxy comes exclusively from the deployment-owned [proxy].url —
//     HTTP_PROXY/HTTPS_PROXY/ALL_PROXY/NO_PROXY environment variables are
//     never consulted and can never produce a direct connection;
//   - an empty or invalid proxy URL is a typed configuration error: the tool
//     reports proxy_required/proxy_invalid while the rest of Iulita keeps
//     running;
//   - compression is disabled: the engine manages Accept-Encoding per
//     representation (identity for downloads, explicit handling inline).
//
// The returned transport is shared; per-run state (cookies, deadlines) lives
// in the engine and run state, never in the transport.
func NewProxyOnlyTransport(proxyURL string, limits Limits) (*http.Transport, *ProxyError) {
	if proxyURL == "" {
		return nil, &ProxyError{Code: "proxy_required", Message: "no system proxy configured ([proxy].url is empty); the HTTP tool requires an explicit proxy"}
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, &ProxyError{Code: "proxy_invalid", Message: fmt.Sprintf("parse proxy URL: %v", err)}
	}
	if !supportedProxySchemes[u.Scheme] {
		return nil, &ProxyError{Code: "proxy_invalid", Message: fmt.Sprintf("unsupported proxy scheme %q (supported: http, https, socks5, socks5h)", u.Scheme)}
	}
	if u.Host == "" {
		return nil, &ProxyError{Code: "proxy_invalid", Message: "proxy URL has no host"}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck // keepalive idiom
	// Fixed proxy: the function always returns the configured URL regardless
	// of the request — there is no NO_PROXY escape and no direct fallback.
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		return u, nil
	}
	transport.DisableCompression = true

	transport.DialContext = (&net.Dialer{
		Timeout:   limits.DialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.TLSHandshakeTimeout = limits.TLSHandshakeTimeout
	transport.ResponseHeaderTimeout = limits.ResponseHeaderWait
	transport.ExpectContinueTimeout = time.Second
	// Idle connections to the proxy are fine; a stalled one must not pin a
	// transfer slot forever.
	transport.IdleConnTimeout = 60 * time.Second

	return transport, nil
}

// ProxySafeURL renders a proxy URL without userinfo for logs and status.
func ProxySafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<invalid proxy url>"
	}
	u.User = nil // credentials never appear in any log or status line
	return u.String()
}
