package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// privateRanges is a package-level list of CIDR ranges that are considered
// private/non-routable. Built once at init time to avoid per-call allocation.
var privateRanges = []*net.IPNet{
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
	mustParseCIDR("100.64.0.0/10"), // CGN (Carrier-Grade NAT, RFC 6598)
	mustParseCIDR("fc00::/7"),      // IPv6 unique local
}

// isPrivateIP checks if an IP address is in a private, loopback,
// link-local, or otherwise non-routable range.
func isPrivateIP(ip net.IP) bool {
	// Normalize IPv4-mapped IPv6 (::ffff:192.168.0.1 → 192.168.0.1).
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}

	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}

	return false
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// ssrfControl returns a net.Dialer Control function that blocks connections
// to private IPs at connect time (layer 2 — prevents DNS rebinding).
func ssrfControl(_ string, address string, _ syscall.RawConn) error {
	connHost, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("SSRF: invalid connect address: %w", err)
	}
	connIP := net.ParseIP(connHost)
	if connIP != nil && isPrivateIP(connIP) {
		return fmt.Errorf("SSRF: DNS rebinding detected, connection to %s blocked", connHost)
	}
	return nil
}

// safeDialer wraps net.Dialer with SSRF protection.
// It performs two checks:
// 1. Pre-flight: resolve hostname and reject private IPs.
// 2. Connect-time: custom Control function to catch DNS rebinding.
// No explicit Timeout — the caller's context carries the deadline.
type safeDialer struct{}

// DialContext resolves the target address and rejects connections to private IPs.
func (d *safeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("SSRF: invalid address %q: %w", addr, err)
	}

	// Layer 1: Pre-flight DNS resolution check.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SSRF: DNS lookup for %q: %w", host, err)
	}

	for _, ipAddr := range ips {
		if isPrivateIP(ipAddr.IP) {
			return nil, fmt.Errorf("SSRF: host %q resolves to private address %s", host, ipAddr.IP)
		}
	}

	// Layer 2: Connect-time check (prevents DNS rebinding between resolution and connect).
	inner := net.Dialer{Control: ssrfControl}

	return inner.DialContext(ctx, network, net.JoinHostPort(host, port))
}

// CheckURLSSRF performs application-level SSRF validation on a URL.
// It resolves the hostname and rejects private/loopback/link-local IPs.
// Use this before making requests through a proxy where the dialer-level
// SSRF protection cannot inspect the actual target IP.
func CheckURLSSRF(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("SSRF: invalid URL: %w", err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("SSRF: URL has no host")
	}

	// Check if host is a raw IP literal.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("SSRF: URL targets private address %s", ip)
		}
		return nil
	}

	// Resolve hostname.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("SSRF: DNS lookup for %q: %w", host, err)
	}
	for _, ipAddr := range ips {
		if isPrivateIP(ipAddr.IP) {
			return fmt.Errorf("SSRF: host %q resolves to private address %s", host, ipAddr.IP)
		}
	}
	return nil
}

// ssrfTransport wraps an http.RoundTripper and validates target URLs
// against SSRF rules before forwarding the request.
// Used when a proxy is active — the dialer connects to the proxy (which
// may have a private IP in k8s), so we validate the actual target URL instead.
//
// NOTE: This only performs pre-flight DNS checks (no connect-time control),
// so DNS rebinding between the check and the proxy connection is not blocked.
// This is acceptable when the proxy is a trusted in-cluster component.
type ssrfTransport struct {
	inner http.RoundTripper
}

func (t *ssrfTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := CheckURLSSRF(req.Context(), req.URL.String()); err != nil {
		return nil, err
	}
	return t.inner.RoundTrip(req)
}

// isProxyActive probes whether the transport's Proxy function yields a
// non-nil proxy URL for a representative HTTPS request.
// http.ProxyFromEnvironment is always non-nil as a function pointer, even
// when HTTP_PROXY/HTTPS_PROXY are unset — this check detects whether a
// proxy is actually configured vs merely having the default function set.
func isProxyActive(t *http.Transport) bool {
	if t.Proxy == nil {
		return false
	}
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	if req == nil {
		return false
	}
	proxyURL, err := t.Proxy(req)
	return err == nil && proxyURL != nil
}

// NewSafeHTTPClient returns an HTTP client with SSRF protection.
// All connections to private/loopback/link-local IPs are blocked.
//
// If httpClient is nil, a default client with SSRF dialer is created.
// If httpClient is provided, its transport must be *http.Transport — it is
// cloned and augmented with SSRF protection (preserving proxy, TLS, etc.).
// Panics if httpClient has a non-nil transport that is not *http.Transport.
//
// When an active proxy is detected (the transport's Proxy function returns a
// non-nil URL), the SSRF dialer is NOT used (it would block the proxy's
// private IP in k8s/k3s clusters). Instead, a RoundTripper wrapper validates
// the actual target URL against SSRF rules.
func NewSafeHTTPClient(timeout time.Duration, httpClient *http.Client) *http.Client {
	d := &safeDialer{}

	if httpClient != nil {
		var base *http.Transport
		switch t := httpClient.Transport.(type) {
		case *http.Transport:
			base = t.Clone()
		case nil:
			base = http.DefaultTransport.(*http.Transport).Clone()
		default:
			panic(fmt.Sprintf("web.NewSafeHTTPClient: unsupported transport type %T; only *http.Transport is supported", httpClient.Transport))
		}

		// When an active proxy is configured, the dialer connects to the
		// proxy host (which may resolve to a private IP in k8s clusters).
		// Use URL-level SSRF validation instead of dialer-level checks.
		if isProxyActive(base) {
			return &http.Client{
				Timeout:   timeout,
				Transport: &ssrfTransport{inner: base},
			}
		}

		base.DialContext = d.DialContext
		return &http.Client{
			Timeout:   timeout,
			Transport: base,
		}
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: d.DialContext,
		},
	}
}
