package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const namespace = "wagering"

type Metrics struct {
	registry *prometheus.Registry

	TransactionsTotal     *prometheus.CounterVec
	TransactionDuration   *prometheus.HistogramVec
	DuplicatesTotal       *prometheus.CounterVec
	RetriesTotal          *prometheus.CounterVec
	ConcurrencyConflicts  prometheus.Counter
	DLQMessagesTotal      *prometheus.CounterVec
	MessagesInFlight      prometheus.Gauge
	OutboxPendingEvents   prometheus.Gauge
	OutboxLagSeconds      prometheus.Gauge
	OutboxPublishedTotal  *prometheus.CounterVec
	ReconciliationChecks  *prometheus.CounterVec
	ReconciliationDiffs   prometheus.Counter
	PendingReferenceTotal *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		registry: registry,

		TransactionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "transactions_total",
			Help:      "Wager transactions by kind, resulting status and source.",
		}, []string{"kind", "status", "source"}),

		TransactionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "transaction_duration_seconds",
			Help:      "End to end processing latency of a wager transaction.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"kind", "source"}),

		DuplicatesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "duplicates_total",
			Help:      "Requests recognised as duplicates, by the mechanism that caught them.",
		}, []string{"source", "detected_by"}),

		RetriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "retries_total",
			Help:      "Retried operations by component.",
		}, []string{"operation"}),

		ConcurrencyConflicts: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "concurrency_conflicts_total",
			Help:      "Optimistic locking conflicts detected while updating a wallet.",
		}),

		DLQMessagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "dlq_messages_total",
			Help:      "Messages routed to the dead letter queue, by reason.",
		}, []string{"reason"}),

		MessagesInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "messages_in_flight",
			Help:      "Messages currently being handled by the SQS consumer.",
		}),

		OutboxPendingEvents: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbox_pending_events",
			Help:      "Events committed to the outbox and not published yet.",
		}),

		OutboxLagSeconds: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "outbox_lag_seconds",
			Help:      "Age of the oldest unpublished outbox event.",
		}),

		OutboxPublishedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "outbox_published_total",
			Help:      "Outbox publish attempts by result.",
		}, []string{"result"}),

		ReconciliationChecks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconciliation_checks_total",
			Help:      "Reconciliation runs by result.",
		}, []string{"result"}),

		ReconciliationDiffs: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "reconciliation_divergences_total",
			Help:      "Reconciliation runs where the stored balance differed from the ledger.",
		}),

		PendingReferenceTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "pending_reference_total",
			Help:      "Outcomes of the pending reference worker.",
		}, []string{"outcome"}),
	}

	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.TransactionsTotal,
		m.TransactionDuration,
		m.DuplicatesTotal,
		m.RetriesTotal,
		m.ConcurrencyConflicts,
		m.DLQMessagesTotal,
		m.MessagesInFlight,
		m.OutboxPendingEvents,
		m.OutboxLagSeconds,
		m.OutboxPublishedTotal,
		m.ReconciliationChecks,
		m.ReconciliationDiffs,
		m.PendingReferenceTotal,
	)

	m.preInitialiseSeries()

	return m
}

func (m *Metrics) preInitialiseSeries() {
	kinds := []string{"OPENING", "BET", "WIN", "LOSS", "REFUND", "ROLLBACK"}
	statuses := []string{"PENDING", "PENDING_REFERENCE", "PROCESSED", "REJECTED", "FAILED"}
	sources := []string{"http", "sqs"}

	for _, kind := range kinds {
		for _, source := range sources {
			m.TransactionDuration.WithLabelValues(kind, source)
			for _, status := range statuses {
				m.TransactionsTotal.WithLabelValues(kind, status, source)
			}
		}
	}

	for _, source := range sources {
		for _, detectedBy := range []string{"idempotency_key", "inbox"} {
			m.DuplicatesTotal.WithLabelValues(source, detectedBy)
		}
	}

	for _, operation := range []string{"wallet_update", "sqs_redelivery", "outbox_publish"} {
		m.RetriesTotal.WithLabelValues(operation)
	}

	for _, reason := range []string{"malformed", "hash_mismatch", "permanent_failure"} {
		m.DLQMessagesTotal.WithLabelValues(reason)
	}

	for _, result := range []string{"success", "failure"} {
		m.OutboxPublishedTotal.WithLabelValues(result)
	}

	for _, result := range []string{"consistent", "divergent", "error"} {
		m.ReconciliationChecks.WithLabelValues(result)
	}

	for _, outcome := range []string{"PROCESSED", "REJECTED", "DEFERRED"} {
		m.PendingReferenceTotal.WithLabelValues(outcome)
	}
}

func (m *Metrics) Registry() *prometheus.Registry { return m.registry }
