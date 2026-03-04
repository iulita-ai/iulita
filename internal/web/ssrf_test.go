package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		// Private ranges.
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"192.168.0.1", true},
		{"192.168.1.100", true},

		// Loopback.
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"::1", true},

		// Link-local.
		{"169.254.0.1", true},
		{"169.254.169.254", true}, // AWS metadata endpoint.
		{"fe80::1", true},

		// Unspecified.
		{"0.0.0.0", true},
		{"::", true},

		// Multicast.
		{"224.0.0.1", true},
		{"ff02::1", true},

		// IPv6 unique local.
		{"fd12:3456:789a::1", true},

		// CGN (Carrier-Grade NAT, RFC 6598).
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false}, // just below CGN range

		// Public IPs — should NOT be private.
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"2606:4700::1", false},

		// Edge: 172.15 is NOT private (only 172.16-172.31).
		{"172.15.255.255", false},
		{"172.32.0.1", false},
	}

	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("failed to parse IP %q", tt.ip)
		}
		got := isPrivateIP(ip)
		if got != tt.private {
			t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, got, tt.private)
		}
	}
}

func TestIsPrivateIP_IPv4MappedIPv6(t *testing.T) {
	// ::ffff:192.168.1.1 should be detected as private.
	ip := net.ParseIP("::ffff:192.168.1.1")
	if !isPrivateIP(ip) {
		t.Error("IPv4-mapped IPv6 192.168.1.1 should be private")
	}

	// ::ffff:8.8.8.8 should NOT be private.
	ip = net.ParseIP("::ffff:8.8.8.8")
	if isPrivateIP(ip) {
		t.Error("IPv4-mapped IPv6 8.8.8.8 should not be private")
	}
}

func TestSafeDialer_BlocksLocalhost(t *testing.T) {
	// Start a local test server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Use safe client — should block localhost.
	client := NewSafeHTTPClient(5*time.Second, nil)
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected SSRF error when connecting to localhost")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected SSRF error, got: %v", err)
	}
}

func TestSafeDialer_BlocksPrivateIP(t *testing.T) {
	d := &safeDialer{}
	ctx := context.Background()

	// These should all fail at the pre-flight DNS check.
	privateAddrs := []string{
		"127.0.0.1:80",
		"[::1]:80",
	}

	for _, addr := range privateAddrs {
		_, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			t.Errorf("expected SSRF error for %s", addr)
			continue
		}
		if !strings.Contains(err.Error(), "SSRF") {
			t.Errorf("expected SSRF error for %s, got: %v", addr, err)
		}
	}
}

func TestNewSafeHTTPClient_ReturnsClient(t *testing.T) {
	client := NewSafeHTTPClient(15*time.Second, nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 15*time.Second {
		t.Errorf("timeout = %v, want 15s", client.Timeout)
	}
	if client.Transport == nil {
		t.Fatal("expected custom transport")
	}
}

func TestCheckURLSSRF(t *testing.T) {
	ctx := context.Background()

	// Private IPs should be blocked.
	blocked := []string{
		"http://127.0.0.1/foo",
		"http://[::1]:8080/bar",
		"http://10.0.0.1/internal",
		"http://192.168.1.1/admin",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/cgn",
	}
	for _, u := range blocked {
		if err := CheckURLSSRF(ctx, u); err == nil {
			t.Errorf("CheckURLSSRF(%q) should block, got nil", u)
		}
	}

	// Public IPs should be allowed.
	if err := CheckURLSSRF(ctx, "http://8.8.8.8/dns"); err != nil {
		t.Errorf("CheckURLSSRF(8.8.8.8) should allow, got: %v", err)
	}

	// Invalid URLs.
	if err := CheckURLSSRF(ctx, "://bad"); err == nil {
		t.Error("expected error for invalid URL")
	}
	if err := CheckURLSSRF(ctx, "http:///no-host"); err == nil {
		t.Error("expected error for URL with no host")
	}
}

func TestNewSafeHTTPClient_WithActiveProxy_UsesSSRFTransport(t *testing.T) {
	// When a proxy is actively configured (returns non-nil URL), the safe
	// client should use ssrfTransport (URL-level validation) instead of the
	// safe dialer (which would block the proxy's private IP in k8s clusters).
	proxyURL, _ := url.Parse("http://proxy.internal:8080")
	baseTransport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}
	baseClient := &http.Client{Transport: baseTransport}

	safe := NewSafeHTTPClient(10*time.Second, baseClient)

	if safe.Timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", safe.Timeout)
	}

	// Should be ssrfTransport, not raw *http.Transport.
	st, ok := safe.Transport.(*ssrfTransport)
	if !ok {
		t.Fatalf("expected *ssrfTransport when proxy is active, got %T", safe.Transport)
	}

	// The inner transport should preserve the proxy setting.
	inner, ok := st.inner.(*http.Transport)
	if !ok {
		t.Fatalf("expected inner *http.Transport, got %T", st.inner)
	}
	if inner.Proxy == nil {
		t.Error("expected proxy to be preserved on inner transport")
	}
}

func TestNewSafeHTTPClient_ProxyFromEnvironment_NoEnv_UsesSafeDialer(t *testing.T) {
	// http.ProxyFromEnvironment is always non-nil as a function pointer,
	// but returns nil when HTTP_PROXY/HTTPS_PROXY are unset.
	// isProxyActive should detect this and use the safe dialer.
	baseTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	baseClient := &http.Client{Transport: baseTransport}

	safe := NewSafeHTTPClient(10*time.Second, baseClient)

	// Without proxy env vars, should use *http.Transport (safe dialer), not ssrfTransport.
	_, ok := safe.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport when ProxyFromEnvironment returns nil, got %T", safe.Transport)
	}
}

func TestNewSafeHTTPClient_WithActiveProxy_BlocksPrivateTarget(t *testing.T) {
	// Integration test: client with active proxy should still block private targets.
	proxyURL, _ := url.Parse("http://proxy.internal:8080")
	baseTransport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}
	safe := NewSafeHTTPClient(5*time.Second, &http.Client{Transport: baseTransport})

	req, _ := http.NewRequest("GET", "http://10.0.0.1/internal", nil)
	_, err := safe.Do(req)
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected SSRF block for private target through proxy client, got: %v", err)
	}
}

func TestNewSafeHTTPClient_WithoutProxy_UsesSafeDialer(t *testing.T) {
	// Without a proxy, the safe client should use the dialer-level SSRF check.
	baseTransport := &http.Transport{
		DisableKeepAlives: true, // marker to verify cloning
	}
	baseClient := &http.Client{Transport: baseTransport}

	safe := NewSafeHTTPClient(10*time.Second, baseClient)

	tr, ok := safe.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport without proxy, got %T", safe.Transport)
	}
	if !tr.DisableKeepAlives {
		t.Error("expected DisableKeepAlives=true to be preserved from base transport")
	}

	// Verify SSRF dialer blocks localhost.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := safe.Get(srv.URL)
	if err == nil {
		t.Fatal("expected SSRF error when connecting to localhost")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected SSRF error, got: %v", err)
	}
}

func TestSSRFTransport_BlocksPrivateTarget(t *testing.T) {
	// ssrfTransport should block requests to private IPs even through a proxy.
	inner := &http.Transport{}
	st := &ssrfTransport{inner: inner}

	req, _ := http.NewRequest("GET", "http://10.0.0.1/internal", nil)
	_, err := st.RoundTrip(req)
	if err == nil {
		t.Fatal("expected SSRF error for private target URL")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected SSRF error, got: %v", err)
	}
}
