// Package metrics exposes Prometheus metrics (internal port, never routed by the
// public edge). It implements the observability ports of app and the adapters.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
)

// Prom holds every collector on a private registry.
type Prom struct {
	registry          *prometheus.Registry
	transactions      *prometheus.CounterVec
	latency           *prometheus.HistogramVec
	conflicts         *prometheus.CounterVec
	messages          *prometheus.CounterVec
	references        *prometheus.CounterVec
	outboxPublished   prometheus.Counter
	outboxFailed      *prometheus.CounterVec
	reconciliations   *prometheus.CounterVec
	httpRequests      *prometheus.HistogramVec
	outboxPending     prometheus.Gauge
	outboxOldest      prometheus.Gauge
	dlqDepth          prometheus.Gauge
	pendingReferences prometheus.Gauge
}

// New registers all collectors.
func New() *Prom {
	r := prometheus.NewRegistry()
	f := func(c prometheus.Collector) { r.MustRegister(c) }
	p := &Prom{registry: r,
		transactions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_wager_transactions_total",
			Help: "Concluded submissions by channel, kind, resulting status and replay flag."}, []string{"channel", "kind", "status", "replay"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "jungle_wager_processing_seconds",
			Help: "Submission processing latency (one SQL transaction).", Buckets: prometheus.ExponentialBuckets(0.002, 2, 12)}, []string{"channel"}),
		conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_concurrency_conflicts_total",
			Help: "Lock timeouts, deadlocks, serialization failures and lost optimistic races."}, []string{"operation"}),
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_sqs_messages_total",
			Help: "Ingress messages by outcome (status or DLQ code) and duplicate flag."}, []string{"outcome", "duplicate"}),
		references: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_reference_attempts_total",
			Help: "Pending-reference retries by outcome."}, []string{"outcome"}),
		outboxPublished: prometheus.NewCounter(prometheus.CounterOpts{Name: "jungle_outbox_published_total", Help: "Outbox events published."}),
		outboxFailed: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_outbox_publish_failures_total",
			Help: "Failed publications (dead=true when the event was parked)."}, []string{"dead"}),
		reconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "jungle_reconciliations_total",
			Help: "Wallet reconciliations by result (divergent results are alerts)."}, []string{"result"}),
		httpRequests: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "jungle_http_request_seconds",
			Help: "HTTP requests by route and status.", Buckets: prometheus.ExponentialBuckets(0.002, 2, 12)}, []string{"route", "status"}),
		outboxPending:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "jungle_outbox_pending_events", Help: "Unpublished, non-parked outbox events."}),
		outboxOldest:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "jungle_outbox_oldest_pending_seconds", Help: "Age of the oldest pending outbox event (outbox lag)."}),
		dlqDepth:          prometheus.NewGauge(prometheus.GaugeOpts{Name: "jungle_sqs_dlq_messages", Help: "Messages in the ingress DLQ."}),
		pendingReferences: prometheus.NewGauge(prometheus.GaugeOpts{Name: "jungle_pending_reference_transactions", Help: "Transactions waiting for their reference."}),
	}
	for _, c := range []prometheus.Collector{p.transactions, p.latency, p.conflicts, p.messages, p.references, p.outboxPublished,
		p.outboxFailed, p.reconciliations, p.httpRequests, p.outboxPending, p.outboxOldest, p.dlqDepth, p.pendingReferences,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})} {
		f(c)
	}
	return p
}

// Handler serves the registry in the Prometheus text format.
func (p *Prom) Handler() http.Handler { return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{}) }

// TransactionConcluded implements app.Metrics.
func (p *Prom) TransactionConcluded(channel string, t wagering.Snapshot, replay bool, elapsed time.Duration) {
	p.transactions.WithLabelValues(channel, string(t.Kind), string(t.Status), strconv.FormatBool(replay)).Inc()
	p.latency.WithLabelValues(channel).Observe(elapsed.Seconds())
}

// ConcurrencyConflict implements app.Metrics.
func (p *Prom) ConcurrencyConflict(operation string) { p.conflicts.WithLabelValues(operation).Inc() }

// ReferenceAttempt implements app.Metrics.
func (p *Prom) ReferenceAttempt(outcome string) { p.references.WithLabelValues(outcome).Inc() }

// OutboxPublished implements app.Metrics.
func (p *Prom) OutboxPublished(n int) { p.outboxPublished.Add(float64(n)) }

// OutboxFailed implements app.Metrics.
func (p *Prom) OutboxFailed(dead bool) {
	p.outboxFailed.WithLabelValues(strconv.FormatBool(dead)).Inc()
}

// ReconciliationChecked implements app.Metrics.
func (p *Prom) ReconciliationChecked(consistent bool) {
	result := "consistent"
	if !consistent {
		result = "divergent"
	}
	p.reconciliations.WithLabelValues(result).Inc()
}

// MessageHandled implements sqsconsumer.Observer.
func (p *Prom) MessageHandled(outcome string, duplicate bool) {
	p.messages.WithLabelValues(outcome, strconv.FormatBool(duplicate)).Inc()
}

// ObserveHTTP implements httpapi.RequestObserver.
func (p *Prom) ObserveHTTP(route string, status int, elapsed time.Duration) {
	p.httpRequests.WithLabelValues(route, strconv.Itoa(status)).Observe(elapsed.Seconds())
}

// SetOutboxBacklog updates the outbox lag gauges.
func (p *Prom) SetOutboxBacklog(oldest time.Duration, pending int64) {
	p.outboxOldest.Set(oldest.Seconds())
	p.outboxPending.Set(float64(pending))
}

// SetDLQDepth updates the DLQ gauge.
func (p *Prom) SetDLQDepth(n int64) { p.dlqDepth.Set(float64(n)) }

// SetPendingReferences updates the pending references gauge.
func (p *Prom) SetPendingReferences(n int64) { p.pendingReferences.Set(float64(n)) }
