package geolocation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

// --- Forward geocoding (Nominatim) tests ---

// nominatimTransport redirects Nominatim requests to a test server and
// records the User-Agent (urlRewriter pattern).
type nominatimTransport struct {
	target string
	lastUA string
	hits   int
}

func (nt *nominatimTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	nt.hits++
	nt.lastUA = req.Header.Get("User-Agent")
	newReq := req.Clone(req.Context())
	parsed := strings.Split(strings.TrimPrefix(nt.target, "http://"), "/")
	newReq.URL.Scheme = "http"
	newReq.URL.Host = parsed[0]
	return http.DefaultTransport.RoundTrip(newReq)
}

func newGeocodeSkill(t *testing.T, srvURL string) (*Skill, *nominatimTransport) {
	t.Helper()
	tr := &nominatimTransport{target: srvURL}
	return New(&http.Client{Transport: tr}), tr
}

func resetGeocodeThrottle() {
	geocodeMu.Lock()
	lastGeocodeAt = time.Time{}
	geocodeMu.Unlock()
}

func TestGeocode_OK(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "jsonv2" {
			t.Errorf("unexpected format param: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"lat":"52.5162719","lon":"13.3777254","display_name":"Brandenburg Gate, Pariser Platz 1, 10117 Berlin"}]`)
	}))
	defer srv.Close()

	s, _ := newGeocodeSkill(t, srv.URL)
	out, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"Brandenburg Gate"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	for _, want := range []string{"Brandenburg Gate, Pariser Platz 1", "52.516272, 13.377725", "© OpenStreetMap"} {
		if !strings.Contains(out, want) {
			t.Fatalf("geocode output missing %q: %s", want, out)
		}
	}
}

func TestGeocode_NoResult(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	s, _ := newGeocodeSkill(t, srv.URL)
	out, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"zzz nonexistent place"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "No coordinates found") {
		t.Fatalf("unexpected no-result output: %s", out)
	}
}

func TestGeocode_Disabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("server must not be hit when geocoding is disabled")
	}))
	defer srv.Close()

	s, _ := newGeocodeSkill(t, srv.URL)
	s.OnConfigChanged("skills.geolocation.geocode_enabled", "false") // no cfgStore → uses value
	out, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"Brandenburg Gate"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "turned off on this server") {
		t.Fatalf("unexpected disabled output: %s", out)
	}
}

func TestGeocode_Throttle(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"lat":"1","lon":"2","display_name":"x"}]`)
	}))
	defer srv.Close()

	s, tr := newGeocodeSkill(t, srv.URL)
	if _, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"first"}`)); err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if tr.hits != 1 {
		t.Fatalf("hits after first = %d", tr.hits)
	}
	// Immediate second call must short-circuit without HTTP (lastGeocodeAt ≈ now).
	out, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"second"}`))
	if err != nil {
		t.Fatalf("second call error: %v", err)
	}
	if !strings.Contains(out, "busy") {
		t.Fatalf("expected busy refusal, got: %s", out)
	}
	if tr.hits != 1 {
		t.Fatalf("throttle did not prevent HTTP: hits = %d", tr.hits)
	}
}

func TestGeocode_UserAgent(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	s, tr := newGeocodeSkill(t, srv.URL)
	if _, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geocode","query":"x"}`)); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if tr.lastUA != nominatimUA {
		t.Fatalf("User-Agent = %q, want %q", tr.lastUA, nominatimUA)
	}
}

// fakeCfgStore satisfies the geolocation configReader interface.
type fakeCfgStore struct {
	val    string
	exists bool
}

func (f fakeCfgStore) GetEffective(string) (string, bool) { return f.val, f.exists }

func TestOnConfigChanged_GeocodeEnabled(t *testing.T) {
	s := New(nil)

	// Explicit false via store → disabled.
	s.SetReloader(fakeCfgStore{val: "false", exists: true})
	s.OnConfigChanged("skills.geolocation.geocode_enabled", "")
	s.mu.RLock()
	enabled := s.geocodeEnabled
	s.mu.RUnlock()
	if enabled {
		t.Fatal("explicit false must disable geocoding")
	}

	// Deletion (key absent) → re-enables (registry quirk: deletion = enabled).
	s.SetReloader(fakeCfgStore{exists: false})
	s.OnConfigChanged("skills.geolocation.geocode_enabled", "")
	s.mu.RLock()
	enabled = s.geocodeEnabled
	s.mu.RUnlock()
	if !enabled {
		t.Fatal("deletion must re-enable geocoding")
	}
}

func TestInputSchemaIncludesGeocode(t *testing.T) {
	s := New(nil)
	schema := string(s.InputSchema())
	for _, want := range []string{`"ip"`, `"action"`, `"query"`, `"geocode"`} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %s: %s", want, schema)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal(s.InputSchema(), &parsed); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	if parsed["type"] != "object" {
		t.Fatal("schema top-level type must stay object")
	}
	if _, required := parsed["required"]; required {
		t.Fatal("ip must stay optional (required array must not appear)")
	}
}

// TestGeocodeDisabledNormalization covers free-text kill-switch values that
// must disable geocoding (UI free-text input like "False"/"off").
func TestGeocodeDisabledNormalization(t *testing.T) {
	for _, v := range []string{"false", "False", "0", "no", "off", "OFF"} {
		s := New(nil)
		s.OnConfigChanged("skills.geolocation.geocode_enabled", v)
		s.mu.RLock()
		enabled := s.geocodeEnabled
		s.mu.RUnlock()
		if enabled {
			t.Fatalf("value %q must disable geocoding", v)
		}
	}
	for _, v := range []string{"true", "1", "yes"} {
		s := New(nil)
		s.OnConfigChanged("skills.geolocation.geocode_enabled", v)
		s.mu.RLock()
		enabled := s.geocodeEnabled
		s.mu.RUnlock()
		if !enabled {
			t.Fatalf("value %q must keep geocoding enabled", v)
		}
	}
}

// TestSetReloaderSeedsKillSwitch verifies base-config values are honored at
// wiring time, not only on runtime changes.
func TestSetReloaderSeedsKillSwitch(t *testing.T) {
	s := New(nil)
	s.SetReloader(fakeCfgStore{val: "false", exists: true})
	s.mu.RLock()
	enabled := s.geocodeEnabled
	s.mu.RUnlock()
	if enabled {
		t.Fatal("SetReloader must seed geocodeEnabled=false from effective config")
	}
}

func TestGeocodeActionNormalization(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"lat":"1.5","lon":"2.5","display_name":"x"}]`)
	}))
	defer srv.Close()

	for _, action := range []string{"geocode", "Geocode", " geocode "} {
		resetGeocodeThrottle()
		s, _ := newGeocodeSkill(t, srv.URL)
		out, err := s.Execute(context.Background(), json.RawMessage(
			fmt.Sprintf(`{"action":%q,"query":"Brandenburg Gate"}`, action)))
		if err != nil {
			t.Fatalf("action %q: Execute error: %v", action, err)
		}
		if strings.Contains(out, "IP") || !strings.Contains(out, "1.500000") {
			t.Fatalf("action %q did not reach the geocode branch: %s", action, out)
		}
	}

	// Unknown non-empty action must not silently run the IP path.
	s, _ := newGeocodeSkill(t, srv.URL)
	out, err := s.Execute(context.Background(), json.RawMessage(`{"action":"geo","query":"x"}`))
	if err != nil {
		t.Fatalf("unknown action: Execute error: %v", err)
	}
	if !strings.Contains(out, "Unknown action") {
		t.Fatalf("unknown action fell through to IP path: %s", out)
	}
}

// TestGeocodeQueryWithoutAction covers the misformed-call guard: query with no
// action must reach the geocode branch, never the server-IP lookup.
func TestGeocodeQueryWithoutAction(t *testing.T) {
	resetGeocodeThrottle()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"lat":"48.8584","lon":"2.2945","display_name":"Eiffel Tower"}]`)
	}))
	defer srv.Close()

	s, _ := newGeocodeSkill(t, srv.URL)
	out, err := s.Execute(context.Background(), json.RawMessage(`{"query":"Eiffel Tower"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(out, "Eiffel Tower") || strings.Contains(out, "IP") {
		t.Fatalf("query-without-action did not reach geocode: %s", out)
	}
}
