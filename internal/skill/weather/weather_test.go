package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/skill/interact"
)

// --- Mock backends ---

type mockBackend struct {
	name   string
	result *WeatherResult
	err    error
}

func (m *mockBackend) Name() string { return m.name }
func (m *mockBackend) Fetch(_ context.Context, _ string, _ int) (*WeatherResult, error) {
	return m.result, m.err
}

// --- Mock PromptAsker ---

type mockAsker struct {
	answer string
	err    error
}

func (m *mockAsker) Ask(_ context.Context, _ string, _ []interact.Option) (string, error) {
	return m.answer, m.err
}

// --- Mock Storage (minimal) ---
// The tests use the weather skill directly with mock backends,
// bypassing storage. For location resolver tests, we test the
// helper functions directly.

// --- Tests ---

func TestWeatherSkill_Metadata(t *testing.T) {
	s := New(nil, nil, nil)

	if s.Name() != "weather" {
		t.Errorf("expected 'weather', got %q", s.Name())
	}
	if s.Description() == "" {
		t.Error("expected non-empty description")
	}
	schema := s.InputSchema()
	if len(schema) == 0 {
		t.Error("expected non-empty schema")
	}
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Errorf("schema is not valid JSON: %v", err)
	}
}

func TestWeatherSkill_WithLocation(t *testing.T) {
	// Use a mock server for Open-Meteo.
	geoSrv := fakeOpenMeteoServer(t)
	defer geoSrv.Close()

	client := &http.Client{
		Transport: &weatherRewriter{openMeteoURL: geoSrv.URL},
	}
	s := New(nil, nil, client)

	result, err := s.Execute(context.Background(), json.RawMessage(`{"location":"Berlin","days":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Berlin") {
		t.Errorf("expected 'Berlin' in result, got:\n%s", result)
	}
	if !strings.Contains(result, "°C") {
		t.Errorf("expected temperature in result, got:\n%s", result)
	}
}

func TestWeatherSkill_MultiDay(t *testing.T) {
	geoSrv := fakeOpenMeteoServer(t)
	defer geoSrv.Close()

	client := &http.Client{
		Transport: &weatherRewriter{openMeteoURL: geoSrv.URL},
	}
	s := New(nil, nil, client)

	// With markdown caps.
	ctx := channel.WithCaps(context.Background(), channel.CapMarkdown)
	result, err := s.Execute(ctx, json.RawMessage(`{"location":"Berlin","days":3}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have a table header.
	if !strings.Contains(result, "Date") && !strings.Contains(result, "Temp") {
		t.Errorf("expected markdown table in result, got:\n%s", result)
	}
}

func TestWeatherSkill_NoLocation_WithPrompt(t *testing.T) {
	geoSrv := fakeOpenMeteoServer(t)
	defer geoSrv.Close()

	client := &http.Client{
		Transport: &weatherRewriter{openMeteoURL: geoSrv.URL},
	}
	s := New(nil, nil, client)

	// Inject a mock asker that returns "Paris".
	asker := &mockAsker{answer: "Paris"}
	ctx := interact.WithPrompter(context.Background(), asker)

	result, err := s.Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The result should contain something (Paris gets geocoded to our mock).
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestWeatherSkill_NoLocation_PromptFails(t *testing.T) {
	s := New(nil, nil, nil)

	// NoopAsker will return ErrNoPrompter, and no geo skill.
	result, err := s.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Unable to determine location") {
		t.Errorf("expected error message, got:\n%s", result)
	}
}

func TestWeatherSkill_InvalidJSON(t *testing.T) {
	s := New(nil, nil, nil)
	_, err := s.Execute(context.Background(), json.RawMessage(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestWeatherSkill_OnConfigChanged(t *testing.T) {
	s := New(nil, nil, nil)

	// Irrelevant key.
	s.OnConfigChanged("skills.other.key", "value")
	s.mu.RLock()
	if s.owmKey != "" {
		t.Error("expected empty owmKey after irrelevant config change")
	}
	s.mu.RUnlock()

	// Relevant key.
	s.OnConfigChanged("skills.weather.openweathermap_api_key", "test-key-123")
	s.mu.RLock()
	if s.owmKey != "test-key-123" {
		t.Errorf("expected 'test-key-123', got %q", s.owmKey)
	}
	s.mu.RUnlock()
}

func TestWeatherSkill_BackendFallback(t *testing.T) {
	failing := &mockBackend{name: "primary", err: fmt.Errorf("service down")}
	working := &mockBackend{
		name: "fallback",
		result: &WeatherResult{
			Location: "Test City",
			Days: []DayForecast{{
				Date:        time.Now(),
				TempMinC:    10,
				TempMaxC:    20,
				Description: "Sunny",
			}},
		},
	}

	chain := newFallbackChain(failing, working)
	result, err := chain.Fetch(context.Background(), "anywhere", 1)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if result.Location != "Test City" {
		t.Errorf("expected 'Test City', got %q", result.Location)
	}
}

func TestWeatherSkill_AllBackendsFail(t *testing.T) {
	b1 := &mockBackend{name: "b1", err: fmt.Errorf("fail1")}
	b2 := &mockBackend{name: "b2", err: fmt.Errorf("fail2")}

	chain := newFallbackChain(b1, b2)
	_, err := chain.Fetch(context.Background(), "anywhere", 1)
	if err == nil {
		t.Fatal("expected error when all backends fail")
	}
	if !strings.Contains(err.Error(), "all weather backends failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest()
	if err != nil {
		t.Fatalf("failed to load manifest: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil manifest")
	}
	if m.Name != "weather" {
		t.Errorf("expected name 'weather', got %q", m.Name)
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
}

func TestFormatForecast_SingleDay(t *testing.T) {
	result := &WeatherResult{
		Location: "Berlin, Germany",
		Days: []DayForecast{{
			Date:        time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC),
			TempMinC:    5,
			TempMaxC:    15,
			Description: "Partly cloudy",
			PrecipMM:    2.5,
			WindKph:     20,
			Humidity:    65,
			UVIndex:     3,
		}},
	}

	out := formatForecast(result, 0) // no caps = plain text
	for _, want := range []string{"Berlin", "Partly cloudy", "15", "5", "°C"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestFormatForecast_MultiDay_Markdown(t *testing.T) {
	result := &WeatherResult{
		Location: "Berlin",
		Days: []DayForecast{
			{Date: time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC), TempMinC: 5, TempMaxC: 15, Description: "Sunny"},
			{Date: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), TempMinC: 3, TempMaxC: 12, Description: "Rain"},
		},
	}

	out := formatForecast(result, channel.CapMarkdown)
	if !strings.Contains(out, "| Date") {
		t.Errorf("expected markdown table, got:\n%s", out)
	}
	if !strings.Contains(out, "Sunny") || !strings.Contains(out, "Rain") {
		t.Errorf("expected both days in output, got:\n%s", out)
	}
}

func TestFormatForecast_PlainText(t *testing.T) {
	result := &WeatherResult{
		Location: "Tokyo",
		Days: []DayForecast{
			{Date: time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC), TempMinC: 10, TempMaxC: 22, Description: "Clear"},
			{Date: time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), TempMinC: 12, TempMaxC: 25, Description: "Cloudy"},
		},
	}

	out := formatForecast(result, 0) // no markdown
	if strings.Contains(out, "| Date") {
		t.Errorf("expected plain text, not markdown table, got:\n%s", out)
	}
	if !strings.Contains(out, "Clear") || !strings.Contains(out, "Cloudy") {
		t.Errorf("expected both days in output, got:\n%s", out)
	}
}

func TestWMODescription(t *testing.T) {
	if got := WMODescription(0); got != "Clear sky" {
		t.Errorf("expected 'Clear sky', got %q", got)
	}
	if got := WMODescription(95); got != "Thunderstorm" {
		t.Errorf("expected 'Thunderstorm', got %q", got)
	}
	if got := WMODescription(999); !strings.Contains(got, "999") {
		t.Errorf("expected code in description for unknown, got %q", got)
	}
}

func TestExtractField(t *testing.T) {
	text := "IP: 1.2.3.4\nCountry: Germany (DE)\nCity: Berlin\nTimezone: Europe/Berlin"
	if got := extractField(text, "City:"); got != "Berlin" {
		t.Errorf("expected 'Berlin', got %q", got)
	}
	if got := extractField(text, "Country:"); got != "Germany" {
		t.Errorf("expected 'Germany', got %q", got)
	}
	if got := extractField(text, "Missing:"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestCleanLocationForGeocoding(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Санкт-Петербурге (СПБ)", "СПБ"},
		{"Москве (МСК)", "МСК"},
		{"Санкт-Петербурге", "Санкт-Петербург"},
		{"Иркутске", "Иркутск"},
		{"Berlin", "Berlin"},
		{"Москва", "Москва"},
	}
	for _, tt := range tests {
		got := cleanLocationForGeocoding(tt.input)
		if got != tt.want {
			t.Errorf("cleanLocationForGeocoding(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractCityFromTimezone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Europe/Amsterdam", "Amsterdam"},
		{"Asia/Tokyo", "Tokyo"},
		{"America/New_York", "New York"},
		{"timezone: Europe/Berlin", "Berlin"},
		{"no timezone here", ""},
		{"just/path/thing", ""},
	}
	for _, tt := range tests {
		got := extractCityFromTimezone(tt.input)
		if got != tt.want {
			t.Errorf("extractCityFromTimezone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractLocationFromFact(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"User lives in Berlin, Germany", "Berlin, Germany"},
		{"User is based in Tokyo", "Tokyo"},
		{"I am from Paris", "Paris"},
		{"Пользователь живёт в Амстердаме", "Амстердаме"},
		{"Живет в Москве", "Москве"},
		{"timezone: Europe/Amsterdam", "Amsterdam"},
		{"User's timezone is Asia/Tokyo", "Tokyo"},
		{"city: London", "city: London"}, // fallback
	}
	for _, tt := range tests {
		got := extractLocationFromFact(tt.input)
		if got != tt.want {
			t.Errorf("extractLocationFromFact(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- Test HTTP servers ---

func fakeOpenMeteoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/v1/search") || strings.Contains(r.URL.RawQuery, "name=") {
			// Geocoding response.
			json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{
					"name":      "Berlin",
					"country":   "Germany",
					"admin1":    "Berlin",
					"latitude":  52.52,
					"longitude": 13.405,
				}},
			})
			return
		}

		// Forecast response.
		json.NewEncoder(w).Encode(map[string]any{
			"daily": map[string]any{
				"time":               []string{"2026-03-14", "2026-03-15", "2026-03-16"},
				"weather_code":       []int{2, 61, 0},
				"temperature_2m_max": []float64{15, 12, 18},
				"temperature_2m_min": []float64{5, 3, 8},
				"precipitation_sum":  []float64{0, 5.2, 0},
				"wind_speed_10m_max": []float64{20, 35, 15},
				"uv_index_max":       []float64{3, 1, 5},
			},
		})
	}))
}

// weatherRewriter redirects Open-Meteo requests to the test server.
type weatherRewriter struct {
	openMeteoURL string
}

func (wr *weatherRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	origURL := req.URL.String()
	var newURL string

	switch {
	case strings.Contains(origURL, "geocoding-api.open-meteo.com"):
		newURL = wr.openMeteoURL + req.URL.Path + "?" + req.URL.RawQuery
	case strings.Contains(origURL, "api.open-meteo.com"):
		newURL = wr.openMeteoURL + req.URL.Path + "?" + req.URL.RawQuery
	case strings.Contains(origURL, "wttr.in"):
		newURL = wr.openMeteoURL + req.URL.Path + "?" + req.URL.RawQuery
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
