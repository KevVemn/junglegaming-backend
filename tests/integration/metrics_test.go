//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

func TestMetricsCountProcessedTransactions(t *testing.T) {
	m := sharedEnv.Metrics

	before := testutil.ToFloat64(
		m.TransactionsTotal.WithLabelValues("BET", "PROCESSED", "http"))

	wallet := openWallet(t, "200.00")
	cmd := betCommand(wallet, "metrics-ok-"+uuid.NewString(), "25.00")
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	after := testutil.ToFloat64(
		m.TransactionsTotal.WithLabelValues("BET", "PROCESSED", "http"))

	if after != before+1 {
		t.Errorf("processed counter = %v, want %v", after, before+1)
	}
}

func TestMetricsCountRejectedTransactions(t *testing.T) {
	m := sharedEnv.Metrics

	before := testutil.ToFloat64(
		m.TransactionsTotal.WithLabelValues("BET", "REJECTED", "http"))

	wallet := openWallet(t, "10.00")
	cmd := betCommand(wallet, "metrics-reject-"+uuid.NewString(), "500.00")
	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if result.State != domain.StateRejected {
		t.Fatalf("state = %s, want REJECTED", result.State)
	}

	after := testutil.ToFloat64(
		m.TransactionsTotal.WithLabelValues("BET", "REJECTED", "http"))

	if after != before+1 {
		t.Errorf("rejected counter = %v, want %v", after, before+1)
	}
}

func TestMetricsCountDuplicates(t *testing.T) {
	m := sharedEnv.Metrics

	before := testutil.ToFloat64(
		m.DuplicatesTotal.WithLabelValues("http", "idempotency_key"))

	wallet := openWallet(t, "200.00")
	cmd := betCommand(wallet, "metrics-dup-"+uuid.NewString(), "25.00")

	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("first submit failed: %v", err)
	}
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	after := testutil.ToFloat64(
		m.DuplicatesTotal.WithLabelValues("http", "idempotency_key"))

	if after != before+1 {
		t.Errorf("duplicate counter = %v, want %v", after, before+1)
	}
}

func TestMetricsCountReconciliationResults(t *testing.T) {
	m := sharedEnv.Metrics

	before := testutil.ToFloat64(
		m.ReconciliationChecks.WithLabelValues("consistent"))

	wallet := openWallet(t, "150.00")
	if _, err := sharedEnv.WalletSvc.Reconcile(context.Background(), wallet.ID()); err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}

	after := testutil.ToFloat64(
		m.ReconciliationChecks.WithLabelValues("consistent"))

	if after != before+1 {
		t.Errorf("consistent counter = %v, want %v", after, before+1)
	}

	if diffs := testutil.ToFloat64(m.ReconciliationDiffs); diffs != 0 {
		t.Errorf("divergence counter = %v, want 0 on a healthy wallet", diffs)
	}
}

func TestMetricsRecordPendingReferenceOutcomes(t *testing.T) {
	m := sharedEnv.Metrics

	before := testutil.ToFloat64(
		m.PendingReferenceTotal.WithLabelValues(string(service.ResumeRejected)))

	wallet := openWallet(t, "100.00")
	cmd := reversalCommand(wallet, domain.KindRollback,
		"metrics-orphan-"+uuid.NewString(), "never-"+uuid.NewString(), "10.00", "round-metrics")

	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	policy := service.ReferencePolicy{MaxAttempts: 1, TTL: 0, BaseBackoff: 1}
	if _, err := sharedEnv.WageringSvc.ResumePendingReference(
		context.Background(), cmd.IdempotencyKey, policy); err != nil {
		t.Fatalf("resume failed: %v", err)
	}

	after := testutil.ToFloat64(
		m.PendingReferenceTotal.WithLabelValues(string(service.ResumeRejected)))

	if after != before+1 {
		t.Errorf("rejected outcome counter = %v, want %v", after, before+1)
	}
}

func TestMetricsRegistryExposesExpectedSeries(t *testing.T) {
	wallet := openWallet(t, "100.00")
	cmd := betCommand(wallet, "metrics-expose-"+uuid.NewString(), "10.00")
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	families, err := sharedEnv.Metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gathering metrics failed: %v", err)
	}

	exposed := make(map[string]bool, len(families))
	for _, f := range families {
		exposed[f.GetName()] = true
	}

	required := []string{
		"wagering_transactions_total",
		"wagering_transaction_duration_seconds",
		"wagering_duplicates_total",
		"wagering_concurrency_conflicts_total",
		"wagering_reconciliation_checks_total",
		"wagering_reconciliation_divergences_total",
		"wagering_outbox_pending_events",
		"wagering_outbox_lag_seconds",
		"wagering_dlq_messages_total",
		"wagering_retries_total",
	}

	for _, name := range required {
		if !exposed[name] {
			t.Errorf("metric %q is not exposed by the registry", name)
		}
	}
}
