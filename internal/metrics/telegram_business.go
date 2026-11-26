package metrics

import (
	"math"
	"time"

	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"github.com/prometheus/client_golang/prometheus"
)

// TelegramBusinessObserver exposes bounded aggregate diagnostics without an
// account, chat, username, connection, provider, error or operation-ID label.
type TelegramBusinessObserver struct {
	events      *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	age         *prometheus.HistogramVec
	reserved    *prometheus.CounterVec
	lastSuccess *prometheus.GaugeVec
}

// NewTelegramBusinessObserver registers Secretary diagnostics in the supplied
// registry. It is constructed once during startup, only when metrics are enabled.
func NewTelegramBusinessObserver(reg prometheus.Registerer) *TelegramBusinessObserver {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &TelegramBusinessObserver{
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "iulita_telegram_business_operations_total", Help: "Account service operations by bounded operation, kind and outcome.",
		}, []string{"operation", "kind", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "iulita_telegram_business_operation_duration_seconds", Help: "Account service operation duration.", Buckets: prometheus.ExponentialBuckets(.01, 4, 9),
		}, []string{"operation"}),
		age: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "iulita_telegram_business_age_seconds", Help: "Observed queue age and rights staleness at admission.", Buckets: prometheus.ExponentialBuckets(1, 4, 10),
		}, []string{"operation"}),
		reserved: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "iulita_telegram_business_reserved_total", Help: "Conservative model reservations; these are not actual billed usage.",
		}, []string{"unit"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "iulita_telegram_business_last_success_timestamp_seconds", Help: "Last successful retention or rights refresh in this process.",
		}, []string{"operation", "kind"}),
	}
	reg.MustRegister(m.events, m.duration, m.age, m.reserved, m.lastSuccess)
	return m
}

func businessCategory(value string, allowed ...string) string {
	for _, item := range allowed {
		if value == item {
			return item
		}
	}
	return "other"
}

// ObserveBusiness validates every category again at the metrics boundary, even
// if a caller passes an unexpected string. Raw errors never become label values.
func (m *TelegramBusinessObserver) ObserveBusiness(o business.Observation) {
	op := businessCategory(o.Operation, "ingress", "reconcile", "execute", "process", "generation", "queue", "consumer", "retention", "reservation")
	kind := businessCategory(o.Kind, "connection", "message", "edit", "delete", "text", "media", "account", "model", "database", "upload", "poll", "candidate", "draft", "compact")
	for _, method := range business.SupportedMethods() {
		if o.Kind == string(method) {
			kind = string(method)
			break
		}
	}
	outcome := businessCategory(o.Outcome, "ok", "denied", "stale", "quota", "unknown", "disabled", "unsupported", "cancelled", "unavailable", "dedup", "quarantined", "conflict", "pending", "expired", "reserved", "release_failed", "cleanup_failed")
	m.events.WithLabelValues(op, kind, outcome).Inc()
	if o.Duration >= 0 {
		m.duration.WithLabelValues(op).Observe(o.Duration.Seconds())
	}
	if o.Age > 0 {
		m.age.WithLabelValues(op).Observe(o.Age.Seconds())
	}
	if outcome == "ok" && (op == "retention" || op == "reconcile") {
		m.lastSuccess.WithLabelValues(op, kind).Set(float64(time.Now().Unix()))
	}
	for unit, value := range map[string]int64{"input_tokens": o.InputTokens, "output_tokens": o.OutputTokens, "cost_micros": o.CostMicros} {
		if value > 0 && value <= math.MaxInt32 {
			m.reserved.WithLabelValues(unit).Add(float64(value))
		}
	}
}
