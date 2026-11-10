package channel

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/iulita-ai/iulita/internal/domain"
)

func TestResponsePresentationSurvivesDerivedContextsAndIsRequestLocal(t *testing.T) {
	ctx, presentation := WithResponsePresentation(context.Background())
	other, independent := WithResponsePresentation(ctx)
	if presentation.Verbatim() || independent.Verbatim() {
		t.Fatal("new response presentation must default to ordinary formatting")
	}
	derived, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			MarkVerbatimResponse(derived)
			_ = presentation.Verbatim()
		}()
	}
	workers.Wait()
	if !presentation.Verbatim() || independent.Verbatim() {
		t.Fatal("mark did not stay within the original request")
	}
	MarkVerbatimResponse(other)
	if !independent.Verbatim() {
		t.Fatal("fresh carrier could not be marked")
	}
	MarkVerbatimResponse(context.Background()) // Other channels need no carrier.
}

func TestVerbatimChunksPreserveRawTextAndUTF16Bounds(t *testing.T) {
	for _, input := range []string{
		"",
		"  <tag attr=\"a&b\"> `literal`\n\n# heading\t  \n",
		strings.Repeat("абв العربية עברית 😀👩‍💻\n  ", 1000),
		strings.Repeat("x", 4095) + "😀\n\n " + strings.Repeat("y", 4100),
	} {
		for _, limit := range []int{2, 3000, 4096} {
			chunks := VerbatimChunks(input, limit)
			if strings.Join(chunks, "") != input {
				t.Fatal("chunking changed text or whitespace")
			}
			for _, chunk := range chunks {
				if chunk == "" || !utf8.ValidString(chunk) || len(utf16.Encode([]rune(chunk))) > limit {
					t.Fatal("invalid or oversized literal chunk")
				}
			}
		}
	}
}

func TestWeatherPresentationDeepCopyAndRequestIsolation(t *testing.T) {
	ctx, p := WithResponsePresentation(context.Background())
	nested, other := WithResponsePresentation(ctx)
	temperature := 0.0
	low := -5.0
	night := false
	card := &domain.WeatherCard{Location: "City", Current: &domain.WeatherCurrent{TemperatureC: &temperature, IsDay: &night}, Days: []domain.WeatherDay{{Date: "2026-11-10", TempMinC: &low}}}
	SetWeatherCard(ctx, card)
	temperature = 100
	card.Location = "mutated"
	low = 999
	got := p.WeatherCard()
	if got.Location != "City" || *got.Current.TemperatureC != 0 || *got.Days[0].TempMinC != -5 {
		t.Fatal("producer aliased trusted payload")
	}
	*got.Current.TemperatureC = 88
	got.Days[0].Date = "mutated"
	if *p.WeatherCard().Current.TemperatureC != 0 || p.WeatherCard().Days[0].Date != "2026-11-10" {
		t.Fatal("consumer aliased trusted payload")
	}
	if other.WeatherCard() != nil {
		t.Fatal("weather crossed turn carrier")
	}
	SetWeatherCard(nested, got)
	SetWeatherCard(ctx, nil)
	if p.WeatherCard() != nil || other.WeatherCard() == nil {
		t.Fatal("clear crossed request boundary")
	}
	SetWeatherCard(context.Background(), card) // noncarrier paths need no state.
}
func TestWeatherPresentationConcurrentSnapshots(t *testing.T) {
	ctx, p := WithResponsePresentation(context.Background())
	var workers sync.WaitGroup
	for i := 0; i < 25; i++ {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			for j := 0; j < 20; j++ {
				v := float64(n)
				SetWeatherCard(ctx, &domain.WeatherCard{Location: "City", Current: &domain.WeatherCurrent{TemperatureC: &v}, Days: []domain.WeatherDay{{TempMinC: &v}}})
				got := p.WeatherCard()
				if got != nil && got.Current != nil {
					*got.Current.TemperatureC = 999
				}
			}
		}(i)
	}
	workers.Wait()
	if got := p.WeatherCard(); got == nil || *got.Current.TemperatureC == 999 {
		t.Fatal("snapshot aliasing")
	}
}
