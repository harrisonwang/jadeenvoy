package obs

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 标签维度集中定义，避免字符串拼写错误。
var (
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "Total HTTP requests by method, path pattern, and status.",
	}, []string{"method", "path", "status"})

	HTTPLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "jadeenvoy",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency by method and path pattern.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"method", "path"})

	SessionsCreated = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "session",
		Name:      "created_total",
		Help:      "Total number of sessions created.",
	})

	SessionEvents = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "session",
		Name:      "events_total",
		Help:      "Total session events by type.",
	}, []string{"type"})

	LLMRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "llm",
		Name:      "requests_total",
		Help:      "Total LLM provider requests by provider name and stop_reason.",
	}, []string{"provider", "stop_reason"})

	LLMLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "jadeenvoy",
		Subsystem: "llm",
		Name:      "request_duration_seconds",
		Help:      "LLM provider call latency.",
		Buckets:   []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"provider"})

	LLMTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "llm",
		Name:      "tokens_total",
		Help:      "LLM tokens consumed by provider and kind (input/output/cache_create/cache_read).",
	}, []string{"provider", "kind"})

	ToolExecs = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "tool",
		Name:      "executions_total",
		Help:      "Total tool executions by name and outcome.",
	}, []string{"name", "outcome"})

	WebhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "jadeenvoy",
		Subsystem: "webhook",
		Name:      "deliveries_total",
		Help:      "Total webhook deliveries by outcome (success / fail / gave_up).",
	}, []string{"outcome"})
)
