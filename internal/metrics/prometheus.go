package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for iulita.
type Metrics struct {
	LLMRequests     *prometheus.CounterVec   // labels: provider, model, status
	LLMTokensInput  *prometheus.CounterVec   // labels: provider
	LLMTokensOutput *prometheus.CounterVec   // labels: provider
	LLMLatency      *prometheus.HistogramVec // labels: provider
	LLMCostUSD      prometheus.Counter
	SkillExecutions *prometheus.CounterVec // labels: skill, status
	TasksTotal      *prometheus.CounterVec // labels: type, status
	MessagesTotal   *prometheus.CounterVec // labels: direction (inbound/outbound)
	CacheHits       *prometheus.CounterVec // labels: cache_type (response/embedding)
	CacheMisses     *prometheus.CounterVec // labels: cache_type
	ActiveSessions  prometheus.Gauge
}

// New registers all Prometheus metrics and returns a Metrics instance.
func New() *Metrics {
	return &Metrics{
		LLMRequests: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "llm",
			Name:      "requests_total",
			Help:      "Total number of LLM requests by provider, model and status.",
		}, []string{"provider", "model", "status"}),

		LLMTokensInput: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "llm",
			Name:      "tokens_input_total",
			Help:      "Total input tokens consumed by provider.",
		}, []string{"provider"}),

		LLMTokensOutput: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "llm",
			Name:      "tokens_output_total",
			Help:      "Total output tokens produced by provider.",
		}, []string{"provider"}),

		LLMLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "iulita",
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "LLM request latency distribution by provider.",
			Buckets:   prometheus.ExponentialBuckets(0.1, 2, 10), // 0.1s to ~51s
		}, []string{"provider"}),

		LLMCostUSD: promauto.NewCounter(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "llm",
			Name:      "cost_usd_total",
			Help:      "Estimated total LLM cost in USD.",
		}),

		SkillExecutions: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "skill",
			Name:      "executions_total",
			Help:      "Total skill executions by skill name and status.",
		}, []string{"skill", "status"}),

		TasksTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "task",
			Name:      "total",
			Help:      "Total tasks by type and final status.",
		}, []string{"type", "status"}),

		MessagesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "messages",
			Name:      "total",
			Help:      "Total messages by direction (inbound/outbound).",
		}, []string{"direction"}),

		CacheHits: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "cache",
			Name:      "hits_total",
			Help:      "Total cache hits by cache type.",
		}, []string{"cache_type"}),

		CacheMisses: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: "iulita",
			Subsystem: "cache",
			Name:      "misses_total",
			Help:      "Total cache misses by cache type.",
		}, []string{"cache_type"}),

		ActiveSessions: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: "iulita",
			Name:      "active_sessions",
			Help:      "Number of currently active chat sessions.",
		}),
	}
}
