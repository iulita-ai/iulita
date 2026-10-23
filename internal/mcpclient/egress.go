package mcpclient

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

// pinnedProxy resolves before contacting the deployment-owned proxy and sends
// only a validated numeric destination to CONNECT/SOCKS. A second, proxy-side
// DNS lookup can therefore never change the destination to a private address.
// TLS still verifies the original hostname and HTTP retains its original Host.
type pinnedProxy struct {
	base   *http.Transport
	lookup func(context.Context, string) ([]net.IPAddr, error)
	mu     sync.Mutex
	pools  map[string]*http.Transport
}

// RoundTrip validates DNS answers and preserves the original TLS/HTTP identity.
func (p *pinnedProxy) RoundTrip(r *http.Request) (*http.Response, error) {
	if _, err := validateEndpoint(r.URL.String()); err != nil {
		return nil, ErrDenied
	}
	host := r.URL.Hostname()
	if p.base.Proxy == nil {
		return nil, ErrUnavailable
	}
	proxyURL, proxyErr := p.base.Proxy(r)
	if proxyErr != nil || proxyURL == nil {
		return nil, ErrUnavailable
	}
	var addresses []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IPAddr{{IP: ip}}
	} else {
		lookup := p.lookup
		if lookup == nil {
			lookup = net.DefaultResolver.LookupIPAddr
		}
		var err error
		addresses, err = lookup(r.Context(), host)
		if err != nil || len(addresses) == 0 {
			return nil, ErrUnavailable
		}
	}
	// Reject mixed public/private answers as well as an entirely private name.
	for _, address := range addresses {
		if address.Zone != "" || !publicIP(address.IP) {
			return nil, ErrDenied
		}
	}
	address := net.JoinHostPort(addresses[0].IP.String(), "443")
	key := host + "|" + address
	p.mu.Lock()
	if p.pools == nil {
		p.pools = map[string]*http.Transport{}
	}
	transport := p.pools[key]
	if transport == nil {
		// Bound rotation of DNS answers and idle connection pools. Closing idle
		// connections does not abort another operation already using a pool.
		if len(p.pools) >= 64 {
			for k, old := range p.pools {
				old.CloseIdleConnections()
				delete(p.pools, k)
				break
			}
		}
		transport = p.base.Clone()
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.ServerName = host
		if proxyURL.Scheme == "https" {
			// net/http otherwise uses the destination ServerName for the first
			// TLS hop too. Verify the HTTPS proxy separately from the MCP server.
			proxyTLS := transport.TLSClientConfig.Clone()
			proxyTLS.ServerName = proxyURL.Hostname()
			proxyTLS.NextProtos = []string{"http/1.1"}
			port := proxyURL.Port()
			if port == "" {
				port = "443"
			}
			proxyAddress := net.JoinHostPort(proxyURL.Hostname(), port)
			dial := transport.DialContext
			handshakeTimeout := transport.TLSHandshakeTimeout
			if handshakeTimeout <= 0 {
				handshakeTimeout = 10 * time.Second
			}
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != proxyAddress {
					return nil, ErrDenied
				}
				connection, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				secured := tls.Client(connection, proxyTLS)
				handshake, cancel := context.WithTimeout(ctx, handshakeTimeout)
				defer cancel()
				if err := secured.HandshakeContext(handshake); err != nil {
					closeQuietly(connection)
					return nil, err
				}
				return secured, nil
			}
		}
		p.pools[key] = transport
	}
	p.mu.Unlock()
	q := r.Clone(r.Context())
	// Even a configured X-Idempotency-Key must not let net/http replay a
	// body-bearing MCP invocation after a reused connection loses its reply.
	q.GetBody = nil
	u := *r.URL
	u.Host = address
	q.URL = &u
	q.Host = r.URL.Host
	return transport.RoundTrip(q)
}

// CloseIdleConnections drains every retained destination pool on shutdown.
func (p *pinnedProxy) CloseIdleConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, transport := range p.pools {
		transport.CloseIdleConnections()
	}
}
