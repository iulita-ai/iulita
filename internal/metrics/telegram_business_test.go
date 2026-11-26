package metrics

import (
	"strings"
	"testing"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

func TestTelegramBusinessMetricsNeverExposeUnboundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	observer := NewTelegramBusinessObserver(reg)
	secret := "PRIVATE_OWNER_CONNECTION_PROVIDER_ERROR"
	observer.ObserveBusiness(business.Observation{Operation: secret, Kind: secret, Outcome: secret})
	observer.ObserveBusiness(business.Observation{Operation: "execute", Kind: "sendMessage", Outcome: "unknown", Duration: time.Second})
	observer.ObserveBusiness(business.Observation{Operation: "reservation", Kind: "model", Outcome: "reserved", InputTokens: 16000, OutputTokens: 1000})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, family := range families {
		if _, encodeErr := expfmt.MetricFamilyToText(&out, family); encodeErr != nil {
			t.Fatal(encodeErr)
		}
	}
	text := out.String()
	if strings.Contains(text, secret) || strings.Contains(text, "owner_id=") || strings.Contains(text, "connection_id=") {
		t.Fatal("private metric label escaped allowlist")
	}
	if !strings.Contains(text, `kind="sendMessage",operation="execute",outcome="unknown"} 1`) || !strings.Contains(text, `unit="input_tokens"} 16000`) {
		t.Fatal("real outcome or reserved quota missing")
	}
}
