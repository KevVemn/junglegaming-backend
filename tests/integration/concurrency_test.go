//go:build integration

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

func TestSameBetSentFiftyTimesInParallelDebitsOnce(t *testing.T) {
	wallet := openWallet(t, "1000.00")
	cmd := betCommand(wallet, "parallel-"+wallet.ID().String(), "25.00")

	const attempts = 50
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		results  []*service.SubmitResult
		failures []error
	)

	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			results = append(results, result)
		}()
	}
	wg.Wait()

	for _, err := range failures {
		if !errors.Is(err, postgres.ErrConcurrencyConflict) {
			t.Errorf("unexpected failure: %v", err)
		}
	}

	if len(results) == 0 {
		t.Fatal("no attempt succeeded")
	}

	firstID := results[0].TransactionID
	for _, r := range results {
		if r.TransactionID != firstID {
			t.Errorf("different transaction ids returned: %s and %s", firstID, r.TransactionID)
		}
	}

	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want exactly 1", got)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "975.00" {
		t.Errorf("balance = %s, want 975.00", balance)
	}
	assertReconciled(t, wallet.ID())
}

func TestTwoBetsOfEightyOnHundredBalance(t *testing.T) {
	wallet := openWallet(t, "100.00")

	cmdA := betCommand(wallet, "contest-a-"+wallet.ID().String(), "80.00")
	cmdB := betCommand(wallet, "contest-b-"+wallet.ID().String(), "80.00")

	type outcome struct {
		result *service.SubmitResult
		err    error
	}
	results := make([]outcome, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r, err := sharedEnv.WageringSvc.Submit(context.Background(), cmdA)
		results[0] = outcome{r, err}
	}()
	go func() {
		defer wg.Done()
		r, err := sharedEnv.WageringSvc.Submit(context.Background(), cmdB)
		results[1] = outcome{r, err}
	}()
	wg.Wait()

	processed, rejected := 0, 0
	for _, o := range results {
		if o.err != nil {
			t.Fatalf("unexpected error: %v", o.err)
		}
		switch o.result.State {
		case domain.StateProcessed:
			processed++
		case domain.StateRejected:
			rejected++
			if o.result.FailureCode != domain.FailureInsufficientFunds {
				t.Errorf("failure code = %s, want INSUFFICIENT_FUNDS", o.result.FailureCode)
			}
		}
	}

	if processed != 1 || rejected != 1 {
		t.Errorf("processed=%d rejected=%d, want exactly 1 and 1", processed, rejected)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "20.00" {
		t.Errorf("balance = %s, want 20.00", balance)
	}
	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want exactly 1", got)
	}
	assertReconciled(t, wallet.ID())
}

func TestReplayAfterContestKeepsSameOutcome(t *testing.T) {
	wallet := openWallet(t, "100.00")
	cmdA := betCommand(wallet, "replay-a-"+wallet.ID().String(), "80.00")
	cmdB := betCommand(wallet, "replay-b-"+wallet.ID().String(), "80.00")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = sharedEnv.WageringSvc.Submit(context.Background(), cmdA) }()
	go func() { defer wg.Done(); _, _ = sharedEnv.WageringSvc.Submit(context.Background(), cmdB) }()
	wg.Wait()

	balanceAfterContest := walletBalance(t, wallet.ID())

	for i := 0; i < 5; i++ {
		if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmdA); err != nil {
			t.Fatalf("replay of A failed: %v", err)
		}
		if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmdB); err != nil {
			t.Fatalf("replay of B failed: %v", err)
		}
	}

	if balance := walletBalance(t, wallet.ID()); !balance.Equal(balanceAfterContest) {
		t.Errorf("balance changed on replay: %s -> %s", balanceAfterContest, balance)
	}
	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want exactly 1", got)
	}
	assertReconciled(t, wallet.ID())
}

func TestIndependentWalletsProceedInParallel(t *testing.T) {
	const wallets = 8

	created := make([]*domain.Wallet, wallets)
	for i := range created {
		created[i] = openWallet(t, "100.00")
	}

	var wg sync.WaitGroup
	errs := make([]error, wallets)

	wg.Add(wallets)
	for i, wallet := range created {
		go func(idx int, w *domain.Wallet) {
			defer wg.Done()
			cmd := betCommand(w, "parallel-wallets-"+w.ID().String(), "30.00")
			_, errs[idx] = sharedEnv.WageringSvc.Submit(context.Background(), cmd)
		}(i, wallet)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("wallet %d failed: %v", i, err)
		}
	}

	for _, wallet := range created {
		if balance := walletBalance(t, wallet.ID()); balance.String() != "70.00" {
			t.Errorf("wallet %s balance = %s, want 70.00", wallet.ID(), balance)
		}
		assertReconciled(t, wallet.ID())
	}
}

func TestInsufficientFundsDoesNotCreateLedgerEntry(t *testing.T) {
	wallet := openWallet(t, "10.00")
	cmd := betCommand(wallet, "nofunds-"+wallet.ID().String(), "50.00")

	result, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != domain.StateRejected {
		t.Errorf("state = %s, want REJECTED", result.State)
	}
	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 0 {
		t.Errorf("debit entries = %d, want 0", got)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "10.00" {
		t.Errorf("balance = %s, want 10.00", balance)
	}
	assertReconciled(t, wallet.ID())
}
