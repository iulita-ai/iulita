package httpfetch

import (
	"net/http"
	"testing"
)

func TestNewProxyOnlyTransportRequiresProxy(t *testing.T) {
	if _, err := NewProxyOnlyTransport("", DefaultLimits()); err == nil || err.Code != "proxy_required" {
		t.Fatalf("empty proxy: got %v, want proxy_required", err)
	}
	if _, err := NewProxyOnlyTransport("ftp://proxy:21", DefaultLimits()); err == nil || err.Code != "proxy_invalid" {
		t.Fatalf("ftp proxy: got %v, want proxy_invalid", err)
	}
	if _, err := NewProxyOnlyTransport("://broken", DefaultLimits()); err == nil || err.Code != "proxy_invalid" {
		t.Fatalf("broken proxy: got %v, want proxy_invalid", err)
	}

	// Supported schemes construct cleanly.
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		tr, err := NewProxyOnlyTransport(scheme+"://proxy.internal:3128", DefaultLimits())
		if err != nil {
			t.Fatalf("%s proxy: %v", scheme, err)
		}
		if tr.Proxy == nil {
			t.Fatal("Proxy func not set")
		}
		// The proxy function must always answer with the configured URL for
		// ANY request — no NO_PROXY-style exceptions exist.
		req := testReqFor(t, "http://anything.example/")
		u, perr := tr.Proxy(req)
		if perr != nil || u == nil || u.Scheme != scheme || u.Host != "proxy.internal:3128" {
			t.Fatalf("%s: Proxy() = %v, %v", scheme, u, perr)
		}
		req2 := testReqFor(t, "https://other.example/x?y=1")
		u2, _ := tr.Proxy(req2)
		if u2 == nil {
			t.Fatalf("%s: Proxy() returned nil for a second request — direct escape", scheme)
		}
	}
}

func TestProxySafeURLRedactsUserinfo(t *testing.T) {
	got := ProxySafeURL("http://user:pass@proxy:3128")
	if want := "http://proxy:3128"; got != want {
		t.Fatalf("ProxySafeURL = %q, want %q", got, want)
	}
	if got := ProxySafeURL("not a url"); got != "<invalid proxy url>" {
		t.Fatalf("invalid render = %q", got)
	}
}

func testReqFor(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
