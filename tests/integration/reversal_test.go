//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

func reversalCommand(
	wallet *domain.Wallet, kind domain.TransactionKind,
	externalID, referenceID, amount, roundID string,
) service.SubmitCommand {
	parsed, _ := domain.ParseMoney(amount, "BRL")
	return service.SubmitCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PlayerID:              wallet.PlayerID(),
		WalletID:              wallet.ID(),
		RoundID:               roundID,
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 parsed,
		ReferenceExternalID:   referenceID,
		CorrelationID:         uuid.NewString(),
	}
}

func placeBet(t *testing.T, wallet *domain.Wallet, externalID, amount, roundID string) {
	t.Helper()
	parsed := money(t, amount)
	cmd := service.SubmitCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PlayerID:              wallet.PlayerID(),
		WalletID:              wallet.ID(),
		RoundID:               roundID,
		GameID:                "fortune-chimp",
		Kind:                  domain.KindBet,
		Money:                 parsed,
		CorrelationID:         uuid.NewString(),
	}
	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("placing bet failed: %v", err)
	}
	if result.State != domain.StateProcessed {
		t.Fatalf("bet state = %s, want PROCESSED", result.State)
	}
}

func TestRefundCreditsTheBetBack(t *testing.T) {
	wallet := openWallet(t, "500.00")
	round := "round-" + uuid.NewString()
	betID := "bet-" + uuid.NewString()

	placeBet(t, wallet, betID, "100.00", round)
	if balance := walletBalance(t, wallet.ID()); balance.String() != "400.00" {
		t.Fatalf("balance after bet = %s, want 400.00", balance)
	}

	cmd := reversalCommand(wallet, domain.KindRefund, "refund-"+uuid.NewString(), betID, "100.00", round)
	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("refund failed: %v", err)
	}
	if result.State != domain.StateProcessed {
		t.Fatalf("refund state = %s (%s), want PROCESSED", result.State, result.FailureCode)
	}

	if balance := walletBalance(t, wallet.ID()); balance.String() != "500.00" {
		t.Errorf("balance after refund = %s, want 500.00", balance)
	}
	assertReconciled(t, wallet.ID())
}

func TestSecondRefundOnSameBetIsRejected(t *testing.T) {
	wallet := openWallet(t, "500.00")
	round := "round-" + uuid.NewString()
	betID := "bet-" + uuid.NewString()

	placeBet(t, wallet, betID, "100.00", round)

	first := reversalCommand(wallet, domain.KindRefund, "refund-1-"+uuid.NewString(), betID, "100.00", round)
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), first); err != nil {
		t.Fatalf("first refund failed: %v", err)
	}

	second := reversalCommand(wallet, domain.KindRefund, "refund-2-"+uuid.NewString(), betID, "100.00", round)
	result, err := sharedEnv.WageringSvc.Submit(context.Background(), second)
	if err != nil {
		t.Fatalf("second refund returned an error: %v", err)
	}

	if result.State != domain.StateRejected {
		t.Errorf("second refund state = %s, want REJECTED", result.State)
	}
	if result.FailureCode != domain.FailureAlreadyReversed {
		t.Errorf("failure code = %s, want ALREADY_REVERSED", result.FailureCode)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "500.00" {
		t.Errorf("balance = %s, want 500.00 (no double refund)", balance)
	}
	assertReconciled(t, wallet.ID())
}

func TestReversalArrivingBeforeItsReferenceStaysPending(t *testing.T) {
	wallet := openWallet(t, "500.00")
	round := "round-" + uuid.NewString()
	betID := "bet-" + uuid.NewString()

	cmd := reversalCommand(wallet, domain.KindRefund, "early-"+uuid.NewString(), betID, "100.00", round)
	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("submitting early refund failed: %v", err)
	}

	if result.State != domain.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "500.00" {
		t.Errorf("balance moved while pending: %s, want 500.00", balance)
	}

	placeBet(t, wallet, betID, "100.00", round)

	policy := service.ReferencePolicy{
		MaxAttempts: 5,
		TTL:         time.Hour,
		BaseBackoff: 10 * time.Millisecond,
	}

	outcome, err := sharedEnv.WageringSvc.ResumePendingReference(
		context.Background(), cmd.IdempotencyKey, policy)
	if err != nil {
		t.Fatalf("resuming pending reference failed: %v", err)
	}
	if outcome != service.ResumeProcessed {
		t.Fatalf("outcome = %s, want PROCESSED", outcome)
	}

	if balance := walletBalance(t, wallet.ID()); balance.String() != "500.00" {
		t.Errorf("final balance = %s, want 500.00", balance)
	}
	assertReconciled(t, wallet.ID())
}

func TestPendingReferenceExpiresAfterMaxAttempts(t *testing.T) {
	wallet := openWallet(t, "200.00")
	round := "round-" + uuid.NewString()

	cmd := reversalCommand(wallet, domain.KindRollback,
		"orphan-"+uuid.NewString(), "never-exists-"+uuid.NewString(), "50.00", round)

	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("submitting orphan rollback failed: %v", err)
	}
	if result.State != domain.StatePendingReference {
		t.Fatalf("state = %s, want PENDING_REFERENCE", result.State)
	}

	policy := service.ReferencePolicy{
		MaxAttempts: 3,
		TTL:         time.Hour,
		BaseBackoff: time.Millisecond,
	}

	var last service.ResumeOutcome
	for i := 0; i < 5; i++ {
		last, err = sharedEnv.WageringSvc.ResumePendingReference(
			context.Background(), cmd.IdempotencyKey, policy)
		if err != nil {
			t.Fatalf("resume attempt %d failed: %v", i, err)
		}
		if last == service.ResumeRejected {
			break
		}
	}

	if last != service.ResumeRejected {
		t.Fatalf("outcome = %s, want REJECTED after exhausting attempts", last)
	}

	transaction, err := sharedEnv.WageringSvc.GetByExternalID(
		context.Background(), "provider-a", cmd.ExternalTransactionID)
	if err != nil {
		t.Fatalf("reading transaction failed: %v", err)
	}
	if transaction.State() != domain.StateRejected {
		t.Errorf("state = %s, want REJECTED", transaction.State())
	}
	if transaction.FailureCode() != domain.FailureReferenceNotFound {
		t.Errorf("failure code = %s, want REFERENCE_NOT_FOUND", transaction.FailureCode())
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "200.00" {
		t.Errorf("balance = %s, want 200.00 (orphan reversal must not move money)", balance)
	}
	assertReconciled(t, wallet.ID())
}

func TestLossMovesNoMoneyAndCreatesNoLedger(t *testing.T) {
	wallet := openWallet(t, "300.00")
	zero := money(t, "0.00")

	cmd := service.SubmitCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: "loss-" + uuid.NewString(),
		IdempotencyKey:        "provider-a:loss-" + uuid.NewString(),
		PlayerID:              wallet.PlayerID(),
		WalletID:              wallet.ID(),
		RoundID:               "round-loss",
		GameID:                "fortune-chimp",
		Kind:                  domain.KindLoss,
		Money:                 zero,
		CorrelationID:         uuid.NewString(),
	}

	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("submitting loss failed: %v", err)
	}
	if result.State != domain.StateProcessed {
		t.Errorf("state = %s, want PROCESSED", result.State)
	}

	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 0 {
		t.Errorf("debit entries = %d, want 0 for LOSS", got)
	}

	after, err := sharedEnv.WalletSvc.GetByID(context.Background(), wallet.ID())
	if err != nil {
		t.Fatal(err)
	}
	if after.Version() != 1 {
		t.Errorf("version = %d, want 1 (LOSS must not bump it)", after.Version())
	}
	if after.Balance().String() != "300.00" {
		t.Errorf("balance = %s, want 300.00", after.Balance())
	}
	assertReconciled(t, wallet.ID())
}
