package httpfetch

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIncludeRawArtifactStoresOwnerCopy(t *testing.T) {
	proxy := newFakeProxy(cannedResponse{
		status:  200,
		headers: map[string]string{"Content-Type": "application/json", "X-Private": "full-value"},
		body:    `{"items":[]}`,
	})
	srv := httptest.NewServer(http.HandlerFunc(proxy.handler))
	defer srv.Close()

	store := newMemArtifactStore()
	e, perr := NewEngine(EngineConfig{
		ProxyURL: srv.URL, Limits: testLimits(), Policy: Policy{Enabled: true},
		Resolver: fakeResolver{}, Artifacts: store, RawArtifacts: true,
	})
	if perr != nil {
		t.Fatal(perr)
	}

	plan := `{"version":1,"request":{"id":"items","method":"GET","url":"http://api.example.test/v1/items",
		"response":{"mode":"inline","include_raw_artifact":true}}}`
	res := runPlan(t, e, plan)
	if res.Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %s (%+v)", res.Outcome, res.Error)
	}
	st := res.Steps[0]
	if st.Artifact == nil {
		t.Fatalf("no raw artifact stored (err %+v)", st.Error)
	}
	if !st.Artifact.Sensitive {
		t.Fatal("raw artifact must be marked sensitive")
	}

	// The stored document keeps the FULL header value that the LLM-facing
	// projection would never carry, plus the bounded body.
	r, err := store.Open(t.Context(), "u1", st.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, r.Size())
	if _, err := readAll(r, buf); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	var doc struct {
		Headers map[string][]string `json:"headers"`
		Body    struct {
			Text string `json:"text"`
		} `json:"body"`
		Status int `json:"status"`
	}
	if err := json.Unmarshal(buf, &doc); err != nil {
		t.Fatalf("stored doc: %v (%s)", err, buf)
	}
	if got := doc.Headers["X-Private"]; len(got) != 1 || got[0] != "full-value" {
		t.Fatalf("full header missing from raw copy: %+v", doc.Headers)
	}
	if doc.Body.Text != `{"items":[]}` || doc.Status != 200 {
		t.Fatalf("body/status not preserved: %+v", doc)
	}
}

func readAll(r interface {
	Read(p []byte) (int, error)
	Close() error
}, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, nil
		}
	}
	return total, nil
}

// connectProxy is a minimal HTTP CONNECT forward proxy: it observes the
// tunnel requests and pipes bytes blindly (no origin dialing — TLS then
// fails against any self-signed origin, which is exactly what the test
// asserts: the engine tried a CONNECT tunnel, never a direct connection).
type connectProxy struct {
	connects chan string
}

func (cp *connectProxy) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT", 400)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", 500)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	_ = client.Close()
	select {
	case cp.connects <- r.RequestURI:
	default:
	}
	// Advertise success but close immediately: the engine must surface a
	// TLS/transport error, never fall back to a direct connection.
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	_, _ = bufio.NewReader(client).Read(make([]byte, 1))
}

// TestHTTPSOriginUsesConnectTunnel proves HTTPS origins traverse the proxy
// via CONNECT (plan §14: HTTP forward for http origins, CONNECT for https).
func TestHTTPSOriginUsesConnectTunnel(t *testing.T) {
	cp := &connectProxy{connects: make(chan string, 4)}
	srv := httptest.NewServer(http.HandlerFunc(cp.handler))
	defer srv.Close()

	e, perr := NewEngine(EngineConfig{
		ProxyURL: srv.URL, Limits: testLimits(), Policy: Policy{Enabled: true},
		Resolver: fakeResolver{},
	})
	if perr != nil {
		t.Fatal(perr)
	}
	res := runPlan(t, e, `{"version":1,"request":{"url":"https://api.example.test/v1/items"}}`)

	// The tunnel was requested for the origin host:443.
	select {
	case target := <-cp.connects:
		if target != "api.example.test:443" {
			t.Fatalf("CONNECT target = %q", target)
		}
	default:
		t.Fatal("proxy never received a CONNECT request — direct connection?")
	}

	// And the outcome is a transport error (torn-down tunnel), never a
	// successful direct fetch.
	if res.Outcome != OutcomeTransportError {
		t.Fatalf("outcome = %s, want transport_error through the broken tunnel", res.Outcome)
	}
}
