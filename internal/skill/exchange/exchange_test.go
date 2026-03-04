package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is /{BASE} — extract base currency from URL.
		base := r.URL.Path[1:]
		if base == "" {
			base = "USD"
		}

		resp := map[string]any{
			"base": base,
			"date": "2026-03-11",
			"rates": map[string]float64{
				"USD": 1.0,
				"EUR": 0.92,
				"GBP": 0.79,
				"JPY": 148.5,
				"UAH": 41.25,
				"PLN": 3.98,
				"BTC": 0.000015,
			},
		}
		// Adjust rates if base != USD (simplified: just return as-is for test).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func newTestSkill(t *testing.T) (*Skill, *httptest.Server) {
	t.Helper()
	srv := fakeAPI(t)
	s := New(srv.Client())
	// Override the base URL by wrapping fetchRates — instead, we patch at test level.
	return s, srv
}

// testExecute calls Execute but patches the API URL to point at the test server.
func testExecute(t *testing.T, srv *httptest.Server, input string) (string, error) {
	t.Helper()
	// Create skill with custom http client that rewrites the URL.
	client := &http.Client{
		Transport: &rewriteTransport{
			base:    srv.URL + "/",
			wrapped: srv.Client().Transport,
		},
	}
	s := New(client)
	return s.Execute(context.Background(), json.RawMessage(input))
}

// rewriteTransport rewrites requests to the test server.
type rewriteTransport struct {
	base    string
	wrapped http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Extract currency from the original URL path.
	orig := req.URL.String()
	// orig looks like https://api.exchangerate-api.com/v4/latest/USD
	// We want to rewrite to testserver/USD
	parts := len(apiBaseURL)
	currency := ""
	if len(orig) > parts {
		currency = orig[parts:]
	}
	newURL := t.base + currency
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	transport := t.wrapped
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(newReq)
}

func TestExchangeSkill_Metadata(t *testing.T) {
	s := New(nil)
	if s.Name() != "exchange_rate" {
		t.Errorf("expected exchange_rate, got %s", s.Name())
	}
	if s.Description() == "" {
		t.Error("expected non-empty description")
	}
	schema := s.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}
}

func TestExchangeSkill_SingleConversion(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"USD","to":"EUR","amount":100}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "EUR") || !contains(result, "92") {
		t.Errorf("expected EUR conversion with ~92, got: %s", result)
	}
	if !contains(result, "100") {
		t.Errorf("expected amount 100 in result, got: %s", result)
	}
}

func TestExchangeSkill_MultipleTargets(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"USD","to":"EUR,GBP,JPY"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, cur := range []string{"EUR", "GBP", "JPY"} {
		if !contains(result, cur) {
			t.Errorf("expected %s in result, got: %s", cur, result)
		}
	}
}

func TestExchangeSkill_DefaultAmount(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"USD","to":"EUR"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Default amount is 1, rate is 0.92.
	if !contains(result, "1 USD") {
		t.Errorf("expected '1 USD' in result, got: %s", result)
	}
	if !contains(result, "0.92 EUR") {
		t.Errorf("expected '0.92 EUR' in result, got: %s", result)
	}
}

func TestExchangeSkill_CaseInsensitive(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"usd","to":"eur"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "EUR") {
		t.Errorf("expected EUR in result, got: %s", result)
	}
}

func TestExchangeSkill_UnknownCurrency(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"USD","to":"XYZ"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "not found") {
		t.Errorf("expected 'not found' message, got: %s", result)
	}
}

func TestExchangeSkill_MissingFrom(t *testing.T) {
	s := New(nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"to":"EUR"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "source currency") {
		t.Errorf("expected prompt for source currency, got: %s", result)
	}
}

func TestExchangeSkill_MissingTo(t *testing.T) {
	s := New(nil)
	result, err := s.Execute(context.Background(), json.RawMessage(`{"from":"USD"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "target currency") {
		t.Errorf("expected prompt for target currency, got: %s", result)
	}
}

func TestExchangeSkill_InvalidJSON(t *testing.T) {
	s := New(nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExchangeSkill_SmallRate(t *testing.T) {
	srv := fakeAPI(t)
	defer srv.Close()

	result, err := testExecute(t, srv, `{"from":"USD","to":"BTC","amount":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// BTC rate is 0.000015 — should use 6 decimal places.
	if !contains(result, "0.000015") {
		t.Errorf("expected small rate format, got: %s", result)
	}
}

func TestFormatAmount(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{1, "1"},
		{100, "100"},
		{92.35, "92.35"},
		{0.005, "0.005000"},
		{1234.5, "1234.50"},
	}
	for _, tt := range tests {
		got := formatAmount(tt.input)
		if got != tt.expected {
			t.Errorf("formatAmount(%v) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseTargets(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"EUR", []string{"EUR"}},
		{"eur,gbp,jpy", []string{"EUR", "GBP", "JPY"}},
		{" EUR , GBP ", []string{"EUR", "GBP"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := parseTargets(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("parseTargets(%q) = %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i, v := range got {
			if v != tt.expected[i] {
				t.Errorf("parseTargets(%q)[%d] = %q, want %q", tt.input, i, v, tt.expected[i])
			}
		}
	}
}

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}
	if m.Name != "exchange" {
		t.Errorf("expected name 'exchange', got %q", m.Name)
	}
	if m.SystemPrompt == "" {
		t.Error("expected non-empty system prompt")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && containsStr(s, sub)
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
