package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// privateRanges is a package-level list of CIDR ranges that are considered
// private/non-routable. Built once at init time to avoid per-call allocation.
var privateRanges = []*net.IPNet{
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
	mustParseCIDR("100.64.0.0/10"),   // CGN (Carrier-Grade NAT, RFC 6598)
	mustParseCIDR("192.0.0.0/24"),    // IETF protocol assignments (incl. NAT64 bypass)
	mustParseCIDR("198.18.0.0/15"),   // benchmarking (RFC 2544)
	mustParseCIDR("240.0.0.0/4"),     // reserved for future use
	mustParseCIDR("192.0.2.0/24"),    // TEST-NET-1 (documentation)
	mustParseCIDR("198.51.100.0/24"), // TEST-NET-2 (documentation)
	mustParseCIDR("203.0.113.0/24"),  // TEST-NET-3 (documentation)
	mustParseCIDR("fc00::/7"),        // IPv6 unique local
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

	// IPv6 ranges that embed IPv4 addresses must be checked against the
	// embedded v4 too: DNS64/NAT64 (64:ff9b::/96) and 6to4 (2002::/16) can
	// carry private v4 targets behind an innocent-looking v6 address.
	if embedded := embeddedIPv4(ip); embedded != nil {
		return isPrivateIP(embedded)
	}

	return false
}

// embeddedIPv4 extracts an IPv4 address carried inside a transition-range
// IPv6 address, or nil.
func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != 16 {
		return nil
	}
	// 64:ff9b::/96 (NAT64/DNS64 well-known prefix).
	if ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
		allZero(ip[4:12]) {
		return net.IPv4(ip[12], ip[13], ip[14], ip[15])
	}
	// 2002::/16 (6to4): the v4 address occupies bytes 2-5.
	if ip[0] == 0x20 && ip[1] == 0x02 {
		return net.IPv4(ip[2], ip[3], ip[4], ip[5])
	}
	return nil
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
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

// Resolver abstracts DNS resolution so callers (and tests) can inject one.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// ValidateTarget performs the shared pre-network target validation used by
// the strict HTTP engine (plan §7.2): scheme, authority shape, port range,
// literal/alternative numeric IP forms, zone identifiers, and DNS answers
// (empty or mixed public/private answer sets are rejected).
//
// The trusted proxy address itself may be private — this function validates
// the ORIGIN, not the proxy. All checks here are local preflight: they cannot
// close the rebinding gap between local and proxy-side DNS; that guarantee
// belongs to the proxy-side policy (deployment release gate).
func ValidateTarget(ctx context.Context, rawURL string, resolver Resolver) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("SSRF: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("SSRF: scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("SSRF: URL has no host")
	}
	if strings.ContainsAny(host, "% \t\x00\r\n") {
		return fmt.Errorf("SSRF: invalid host (zone identifiers and control characters are rejected)")
	}
	if u.User != nil {
		return fmt.Errorf("SSRF: userinfo in URL is rejected")
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("SSRF: invalid port %q", p)
		}
	}

	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("SSRF: target is a private/special-use address %s", ip)
		}
		return nil
	}

	// Alternative numeric IPv4 forms (0x7f000001, 2130706433, 127.1) parse as
	// hostnames but resolve numerically via getaddrinfo — reject outright
	// instead of racing the resolver.
	if looksNumericHost(host) {
		return fmt.Errorf("SSRF: numeric host %q in non-standard form is rejected", host)
	}

	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, lookupErr := resolver.LookupIPAddr(ctx, host)
	if lookupErr != nil {
		return fmt.Errorf("SSRF: DNS lookup for %q: %w", host, lookupErr)
	}
	if len(ips) == 0 {
		return fmt.Errorf("SSRF: empty DNS answer for %q", host)
	}
	for _, ipAddr := range ips {
		if isPrivateIP(ipAddr.IP) {
			// Mixed public/private answers are rejected with the same rule:
			// any private answer makes the whole name untrustworthy.
			return fmt.Errorf("SSRF: host %q resolves to private/special-use address %s", host, ipAddr.IP)
		}
	}
	return nil
}

// looksNumericHost reports whether a non-IP-literal host is still numeric
// (decimal/hex IPv4 shorthand like "127.1", "0x7f.0.0.1", "2130706433").
func looksNumericHost(host string) bool {
	if host == "" {
		return false
	}
	hex := strings.HasPrefix(strings.ToLower(host), "0x")
	for i := 0; i < len(host); i++ {
		c := host[i]
		digit := c >= '0' && c <= '9'
		dot := c == '.'
		hexDigit := hex && (c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
		if !digit && !dot && !hexDigit {
			return false
		}
	}
	return true
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
