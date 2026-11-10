package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
	"github.com/iulita-ai/iulita/internal/i18n"
	"golang.org/x/text/language"
)

type cardTransport func(*http.Request) (*http.Response, error)

func (f cardTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func cardJSON(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func cardSkill(t *testing.T, forecast string) *Skill {
	t.Helper()
	client := &http.Client{Transport: cardTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/search") {
			return cardJSON(`{"results":[{"name":"Berlin","latitude":52.52,"longitude":13.405}]}`), nil
		}
		return cardJSON(forecast), nil
	})}
	return New(nil, nil, client)
}
func TestWeatherCardRealCurrentAndMissingVersusZero(t *testing.T) {
	body := `{"timezone":"Europe/Berlin","utc_offset_seconds":3600,"current":{"time":"2026-11-10T01:15","interval":900,"temperature_2m":0,"apparent_temperature":null,"relative_humidity_2m":0,"wind_speed_10m":0,"precipitation":0,"weather_code":0,"is_day":0},"daily":{"time":["2026-11-10","2026-11-11","2026-11-12"],"temperature_2m_max":[20,null,8],"temperature_2m_min":[0],"weather_code":[61,null,999],"precipitation_sum":[0],"wind_speed_10m_max":[0],"uv_index_max":[0]}}`
	skill := cardSkill(t, body)
	ctx, p := channel.WithResponsePresentation(context.Background())
	card, err := skill.ForecastCard(ctx, "Berlin", 3)
	if err != nil {
		t.Fatal(err)
	}
	if card.Current == nil || card.Current.TemperatureC == nil || *card.Current.TemperatureC != 0 || card.Current.FeelsLikeC != nil || card.Current.HumidityPercent == nil || *card.Current.HumidityPercent != 0 || card.Current.IsDay == nil || *card.Current.IsDay || card.Current.Condition != domain.WeatherClear {
		t.Fatalf("invented/missing current: %+v", card.Current)
	}
	if card.Days[0].Condition != domain.WeatherRain || card.Days[1].Condition != domain.WeatherUnknown || card.Days[2].Condition != domain.WeatherUnknown || card.Days[1].TempMinC != nil || card.Days[1].TempMaxC != nil || card.Days[2].PrecipitationMM != nil {
		t.Fatal("missing arrays/code became zero/clear", card.Days)
	}
	if card.Current.ObservedAt.Format(time.RFC3339) != "2026-11-10T01:15:00+01:00" || card.Current.IntervalSeconds != 900 {
		t.Fatal("source time/period lost")
	}
	if p.WeatherCard() == nil || p.WeatherCard().Current == nil {
		t.Fatal("native carrier not attached")
	}
	text := FormatCardText(i18n.WithLocale(ctx, language.Russian), card)
	if !strings.Contains(text, "+0°C") || !strings.Contains(text, "0%") || !strings.Contains(text, "0.0 мм") || strings.Contains(text, "IMPORTANT") || !strings.Contains(text, "2026-11-12") {
		t.Fatal(text)
	}
	model, err := skill.Forecast(ctx, "Berlin", 3)
	if err != nil || !strings.Contains(model, "— to —") || strings.Contains(model, "+0..+0") || !strings.Contains(model, "Current model conditions") {
		t.Fatal(model, err)
	}
}
func TestOpenMeteoRequestsActualCurrentAndUnits(t *testing.T) {
	client := &http.Client{Transport: cardTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		for _, field := range []string{"temperature_2m", "apparent_temperature", "relative_humidity_2m", "weather_code", "is_day", "precipitation", "wind_speed_10m"} {
			if !strings.Contains(q.Get("current"), field) {
				t.Error("current field not fetched", field)
			}
		}
		if q.Get("temperature_unit") != "celsius" || q.Get("wind_speed_unit") != "kmh" || q.Get("precipitation_unit") != "mm" || q.Get("timezone") != "auto" {
			t.Error("units/timezone not explicit")
		}
		return cardJSON(`{"daily":{"time":["2026-11-10"],"temperature_2m_max":[10]}}`), nil
	})}
	r, err := NewOpenMeteoBackend(client).Fetch(context.Background(), "52.50000, 13.40000", 1)
	if err != nil || r.Current != nil || r.Days[0].Values.TempMinC != nil || r.Days[0].Values.Condition != domain.WeatherUnknown {
		t.Fatal(r, err)
	}
}
func TestMalformedMetricsAndUnexpectedUnitsStayUnavailable(t *testing.T) {
	for _, body := range []string{
		`{"timezone":"UTC","current_units":{"temperature_2m":"°F"},"current":{"time":"2026-11-10T01:15","temperature_2m":70,"weather_code":999},"daily":{"time":["bad","2026-11-10"],"temperature_2m_min":[4,20],"temperature_2m_max":[3,10]}}`,
		`{"current":{"time":"no-time","temperature_2m":3},"daily":{"time":["2026-11-10"],"temperature_2m_min":[-999],"temperature_2m_max":[999],"weather_code":[45]}}`,
	} {
		r, err := NewOpenMeteoBackend(cardSkill(t, body).httpClient).Fetch(context.Background(), "52.5, 13.4", 1)
		if err != nil {
			t.Fatal(err)
		}
		if r.Current != nil || len(r.Days) != 1 || r.Days[0].Values.TempMinC != nil || r.Days[0].Values.TempMaxC != nil {
			t.Fatal("malformed data became real readings", r)
		}
	}
	if validMetric(value(math.NaN()), -100, 100) != nil || validMetric(value(math.Inf(1)), 0, 100) != nil {
		t.Fatal("nonfinite metric accepted")
	}
}
func TestForecastCardPreservesAllDaysAndClearsOnFailure(t *testing.T) {
	days := []string{}
	temps := []int{}
	for n := 0; n < 16; n++ {
		days = append(days, time.Date(2026, 11, 10+n, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
		temps = append(temps, n)
	}
	raw, _ := json.Marshal(map[string]any{"daily": map[string]any{"time": days, "temperature_2m_max": temps}})
	skill := cardSkill(t, string(raw))
	ctx, p := channel.WithResponsePresentation(context.Background())
	card, err := skill.ForecastCard(ctx, "Berlin", 16)
	if err != nil || len(card.Days) != 16 || card.RequestedDays != 16 {
		t.Fatal(card, err)
	}
	text := FormatCardText(ctx, card)
	if !strings.Contains(text, "2026-11-25") {
		t.Fatal("last day dropped")
	}
	if _, err = skill.ForecastCard(ctx, "", 1); err == nil || p.WeatherCard() != nil {
		t.Fatal("failed attempt kept stale carrier")
	}
	channel.SetWeatherCard(ctx, card)
	_, _ = skill.Execute(ctx, json.RawMessage(`broken`))
	if p.WeatherCard() != nil {
		t.Fatal("invalid input kept stale carrier")
	}
}
func TestFallbackWeatherHasNoFakeCurrentOrRawPin(t *testing.T) {
	body := `{"weather":[{"date":"2026-11-10","maxtempC":"0","mintempC":"-3","uvIndex":"0","hourly":[{"weatherCode":"122","weatherDesc":[{"value":"Cloudy"}],"windspeedKmph":"15","precipMM":"2.0","humidity":"0"}]}]}`
	client := &http.Client{Transport: cardTransport(func(*http.Request) (*http.Response, error) { return cardJSON(body), nil })}
	result, err := NewWttrInBackend(client).Fetch(context.Background(), "52.516270, 13.377750", 16)
	if err != nil {
		t.Fatal(err)
	}
	card := weatherCard(result, "52.516270, 13.377750", 16)
	if card.Current != nil || card.Location != sharedPinDescriptor || card.Days[0].Condition != domain.WeatherCloudy || card.Days[0].TempMaxC == nil || *card.Days[0].TempMaxC != 0 || card.Days[0].PrecipitationMM != nil || card.Days[0].WindKph != nil || card.Days[0].UVIndexMax != nil {
		t.Fatal("sample promoted into current/daily aggregate", card)
	}
	raw, _ := json.Marshal(card)
	model := formatForecast(result, channel.CapMarkdown)
	if strings.Contains(string(raw), "52.516270") || strings.Contains(model, "52.516270") || !strings.Contains(model, "Midday forecast sample") || !strings.Contains(model, "humidity 0%") {
		t.Fatal(string(raw), model)
	}
	human := FormatCardText(context.Background(), card)
	if strings.Contains(human, "IMPORTANT") || !strings.Contains(human, "Forecast only") || !strings.Contains(human, "1/16") {
		t.Fatal(human)
	}
}
func TestOWMUsesForecastLocationDateAndNeverInventsCurrent(t *testing.T) {
	body := fmt.Sprintf(`{"city":{"name":"City","timezone":7200},"list":[{"dt":%d,"main":{"temp_min":0,"temp_max":4,"humidity":0},"weather":[{"id":500,"description":"rain"}],"wind":{"speed":0},"rain":{"3h":0}}]}`, time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC).Unix())
	client := &http.Client{Transport: cardTransport(func(*http.Request) (*http.Response, error) { return cardJSON(body), nil })}
	result, err := NewOWMBackend(client, "fixture-key").Fetch(context.Background(), "City", 1)
	if err != nil {
		t.Fatal(err)
	}
	card := weatherCard(result, "City", 1)
	if card.Current != nil || card.Days[0].Date != "2026-11-10" || card.Days[0].Condition != domain.WeatherRain || card.Days[0].PrecipitationMM != nil || card.Days[0].WindKph == nil || *card.Days[0].WindKph != 0 {
		t.Fatal(card)
	}
	if text := formatForecast(result, 0); !strings.Contains(text, "three-hour forecast periods") || !strings.Contains(text, "precipitation 0.0 mm") {
		t.Fatal(text)
	}
}
func TestEmptyPrimaryDoesNotStopFallbackChain(t *testing.T) {
	empty := &mockBackend{name: "primary", result: &WeatherResult{Days: []DayForecast{{Values: &domain.WeatherDay{Date: "2026-11-10", Condition: domain.WeatherUnknown}}}}}
	good := &mockBackend{name: "fallback", result: &WeatherResult{Days: []DayForecast{{Date: time.Now(), TempMaxC: 3}}}}
	result, err := newFallbackChain(empty, good).Fetch(context.Background(), "City", 1)
	if err != nil || result.Source != "fallback" {
		t.Fatal(result, err)
	}
}

func TestUnknownProviderCodesNeverBecomeSunnyOrAnotherIllustration(t *testing.T) {
	for _, code := range []int{-1, 4, 999} {
		if wmoCondition(&code) != domain.WeatherUnknown {
			t.Fatal("unknown WMO condition", code)
		}
	}
	for _, code := range []int{0, 203, 303, 505, 623, 781, 999} {
		if owmCondition(code) != domain.WeatherUnknown {
			t.Fatal("unknown OWM condition", code)
		}
	}
	if wttrCondition("") != domain.WeatherUnknown || wttrCondition("999") != domain.WeatherUnknown {
		t.Fatal("unknown wttr condition")
	}
}
