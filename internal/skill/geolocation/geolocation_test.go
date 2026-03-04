package geolocation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServers creates test servers for IP detection and geolocation.
func fakeServers(t *testing.T) (ipSrv, geoSrv *httptest.Server) {
	t.Helper()

	ipSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ip": "203.0.113.1"})
	}))

	geoSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":      "success",
			"country":     "Germany",
			"countryCode": "DE",
			"regionName":  "Berlin",
			"city":        "Berlin",
			"timezone":    "Europe/Berlin",
			"isp":         "Deutsche Telekom AG",
			"query":       "203.0.113.1",
		})
	}))

	return ipSrv, geoSrv
}

// fakeGeoFailServer returns a geo server that reports failure for all providers.
func fakeGeoFailServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "fail",
			"message": "reserved range",
			"error":   true,
			"reason":  "rate limited",
		})
	}))
}

// fakeIcanhazServer returns a plain text IP server.
func fakeIcanhazServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("203.0.113.2\n"))
	}))
}

// fakeIPAPICoServer returns an ipapi.co-compatible server.
func fakeIPAPICoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ip":           "203.0.113.1",
			"country_name": "Germany",
			"country_code": "DE",
			"region":       "Berlin",
			"city":         "Berlin",
			"timezone":     "Europe/Berlin",
			"org":          "Deutsche Telekom AG",
		})
	}))
}

// newTestSkill creates a skill with overridden URLs pointing to test servers.
func newTestSkill(t *testing.T, ipSrvURL, geoSrvURL string) *Skill {
	t.Helper()
	client := &http.Client{
		Transport: &urlRewriter{
			ipDetectURL: ipSrvURL,
			geoURL:      geoSrvURL,
		},
	}
	return New(client)
}

// urlRewriter redirects requests to test servers based on the original URL pattern.
type urlRewriter struct {
	ipDetectURL string // test server URL for IP detection
	geoURL      string // test server URL for geolocation
}

func (u *urlRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	origURL := req.URL.String()
	var newURL string

	switch {
	case strings.Contains(origURL, "api.ipify.org"):
		newURL = u.ipDetectURL
	case strings.Contains(origURL, "icanhazip.com"):
		newURL = u.ipDetectURL
	case strings.Contains(origURL, "ip-api.com"):
		newURL = u.geoURL + req.URL.Path
	case strings.Contains(origURL, "ipapi.co"):
		newURL = u.geoURL + req.URL.Path
	case strings.Contains(origURL, "ipinfo.io"):
		newURL = u.geoURL + req.URL.Path
	default:
		newURL = origURL
	}

	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		newReq.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestGeolocationSkill_Metadata(t *testing.T) {
	s := New(nil)

	if s.Name() != "geolocation" {
		t.Errorf("expected 'geolocation', got %q", s.Name())
	}
	if s.Description() == "" {
		t.Error("expected non-empty description")
	}
	schema := s.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}
	// Verify schema is valid JSON.
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Errorf("schema is not valid JSON: %v", err)
	}
}

func TestGeolocationSkill_AutoDetect(t *testing.T) {
	ipSrv, geoSrv := fakeServers(t)
	defer ipSrv.Close()
	defer geoSrv.Close()

	s := newTestSkill(t, ipSrv.URL, geoSrv.URL)
	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"203.0.113.1", "Germany", "DE", "Berlin", "Europe/Berlin", "Deutsche Telekom"} {
		if !strings.Contains(result, want) {
			t.Errorf("expected %q in result, got:\n%s", want, result)
		}
	}
}

func TestGeolocationSkill_ExplicitIP(t *testing.T) {
	_, geoSrv := fakeServers(t)
	defer geoSrv.Close()

	// IP detection server not needed — explicit IP bypasses it.
	s := newTestSkill(t, "http://invalid.test", geoSrv.URL)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"ip":"203.0.113.1"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "203.0.113.1") {
		t.Errorf("expected IP in result, got:\n%s", result)
	}
	if !strings.Contains(result, "Berlin") {
		t.Errorf("expected city in result, got:\n%s", result)
	}
}

func TestGeolocationSkill_InvalidIP(t *testing.T) {
	s := New(nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"ip":"not-an-ip"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Invalid IP") {
		t.Errorf("expected 'Invalid IP' message, got: %s", result)
	}
}

func TestGeolocationSkill_GeoAPIFailure(t *testing.T) {
	ipSrv, _ := fakeServers(t)
	defer ipSrv.Close()

	failSrv := fakeGeoFailServer(t)
	defer failSrv.Close()

	s := newTestSkill(t, ipSrv.URL, failSrv.URL)
	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return a user-friendly message, not an error.
	if !strings.Contains(result, "Unable to determine location") {
		t.Errorf("expected graceful failure message, got:\n%s", result)
	}
}

func TestGeolocationSkill_FallbackIPDetect(t *testing.T) {
	// ipify fails (invalid URL), icanhazip should be used as fallback.
	icanhazSrv := fakeIcanhazServer(t)
	defer icanhazSrv.Close()

	geoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":      "success",
			"country":     "France",
			"countryCode": "FR",
			"regionName":  "Ile-de-France",
			"city":        "Paris",
			"timezone":    "Europe/Paris",
			"isp":         "Orange S.A.",
			"query":       "203.0.113.2",
		})
	}))
	defer geoSrv.Close()

	// Custom transport: ipify → returns 500, icanhazip → icanhazSrv.
	failIPSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failIPSrv.Close()

	client := &http.Client{
		Transport: &fallbackIPRewriter{
			ipifySrvURL:   failIPSrv.URL,
			icanhazSrvURL: icanhazSrv.URL,
			geoSrvURL:     geoSrv.URL,
		},
	}
	s := New(client)

	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Paris") {
		t.Errorf("expected 'Paris' in result, got:\n%s", result)
	}
	if !strings.Contains(result, "203.0.113.2") {
		t.Errorf("expected fallback IP in result, got:\n%s", result)
	}
}

// fallbackIPRewriter routes ipify and icanhazip to different test servers.
type fallbackIPRewriter struct {
	ipifySrvURL   string
	icanhazSrvURL string
	geoSrvURL     string
}

func (f *fallbackIPRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	origURL := req.URL.String()
	var newURL string

	switch {
	case strings.Contains(origURL, "api.ipify.org"):
		newURL = f.ipifySrvURL
	case strings.Contains(origURL, "icanhazip.com"):
		newURL = f.icanhazSrvURL
	case strings.Contains(origURL, "ip-api.com"):
		newURL = f.geoSrvURL + req.URL.Path
	case strings.Contains(origURL, "ipapi.co"):
		newURL = f.geoSrvURL + req.URL.Path
	default:
		newURL = origURL
	}

	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		newReq.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestGeolocationSkill_FallbackGeoProvider(t *testing.T) {
	ipSrv, _ := fakeServers(t)
	defer ipSrv.Close()

	// ip-api.com fails, ipapi.co succeeds.
	failGeoSrv := fakeGeoFailServer(t)
	defer failGeoSrv.Close()

	coSrv := fakeIPAPICoServer(t)
	defer coSrv.Close()

	client := &http.Client{
		Transport: &geoFallbackRewriter{
			ipSrvURL:      ipSrv.URL,
			ipAPIFailURL:  failGeoSrv.URL,
			ipapiCoSrvURL: coSrv.URL,
		},
	}
	s := New(client)

	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Berlin") {
		t.Errorf("expected 'Berlin' from fallback provider, got:\n%s", result)
	}
}

// geoFallbackRewriter routes ip-api.com to a failing server and ipapi.co to a working one.
type geoFallbackRewriter struct {
	ipSrvURL      string
	ipAPIFailURL  string
	ipapiCoSrvURL string
}

func (g *geoFallbackRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	origURL := req.URL.String()
	var newURL string

	switch {
	case strings.Contains(origURL, "api.ipify.org"):
		newURL = g.ipSrvURL
	case strings.Contains(origURL, "ip-api.com"):
		newURL = g.ipAPIFailURL + req.URL.Path
	case strings.Contains(origURL, "ipapi.co"):
		newURL = g.ipapiCoSrvURL + req.URL.Path
	default:
		newURL = origURL
	}

	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Header {
		newReq.Header[k] = v
	}
	return http.DefaultTransport.RoundTrip(newReq)
}

func TestGeolocationSkill_PrivateIP(t *testing.T) {
	s := New(nil)

	privateIPs := []string{
		"192.168.1.1",
		"10.0.0.1",
		"172.16.0.1",
		"169.254.169.254", // AWS IMDS
		"127.0.0.1",
		"100.64.0.1", // CGN
		"::1",
		"fe80::1",
	}
	for _, ip := range privateIPs {
		result, err := s.Execute(context.Background(), json.RawMessage(fmt.Sprintf(`{"ip":"%s"}`, ip)))
		if err != nil {
			t.Fatalf("unexpected error for IP %s: %v", ip, err)
		}
		if !strings.Contains(result, "not a public routable address") {
			t.Errorf("expected rejection for private IP %s, got: %s", ip, result)
		}
	}
}

func TestGeolocationSkill_InvalidJSON(t *testing.T) {
	s := New(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestGeolocationSkill_OnConfigChanged(t *testing.T) {
	s := New(nil)

	// Irrelevant key — should be ignored.
	s.OnConfigChanged("skills.other.key", "value")
	s.mu.RLock()
	if s.apiKey != "" {
		t.Error("expected empty apiKey after irrelevant config change")
	}
	s.mu.RUnlock()

	// Relevant key.
	s.OnConfigChanged("skills.geolocation.api_key", "test-token-123")
	s.mu.RLock()
	if s.apiKey != "test-token-123" {
		t.Errorf("expected 'test-token-123', got %q", s.apiKey)
	}
	s.mu.RUnlock()
}

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest")
	}
	if m.Name != "geolocation" {
		t.Errorf("expected name 'geolocation', got %q", m.Name)
	}
	if m.SystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
	if len(m.ForceTriggers) == 0 {
		t.Error("expected force triggers to be populated")
	}
	if len(m.ConfigKeys) == 0 {
		t.Error("expected config keys to be populated")
	}
	if len(m.SecretKeys) == 0 {
		t.Error("expected secret keys to be populated")
	}
}

func TestFormatResult(t *testing.T) {
	r := &geoResult{
		IP:          "1.2.3.4",
		Country:     "Germany",
		CountryCode: "DE",
		Region:      "Berlin",
		City:        "Berlin",
		Timezone:    "Europe/Berlin",
		ISP:         "Deutsche Telekom AG",
	}
	out := formatResult(r)

	for _, want := range []string{"1.2.3.4", "Germany (DE)", "Berlin", "Europe/Berlin", "Deutsche Telekom AG"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestFormatResult_MinimalFields(t *testing.T) {
	r := &geoResult{
		IP:          "1.2.3.4",
		CountryCode: "US",
	}
	out := formatResult(r)

	if !strings.Contains(out, "1.2.3.4") {
		t.Error("expected IP in output")
	}
	if !strings.Contains(out, "US") {
		t.Error("expected country code in output")
	}
	// Should not have empty lines for missing fields.
	if strings.Contains(out, "Region:") {
		t.Error("should not contain Region when empty")
	}
}
