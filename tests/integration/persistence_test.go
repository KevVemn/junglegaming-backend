//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

func TestLedgerIsAppendOnly(t *testing.T) {
	wallet := openWallet(t, "100.00")
	ctx := context.Background()

	var entryID uuid.UUID
	err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT id FROM wallet_ledger_entries WHERE wallet_id = $1 LIMIT 1`, wallet.ID()).Scan(&entryID)
	if err != nil {
		t.Fatalf("reading ledger entry failed: %v", err)
	}

	t.Run("update is rejected", func(t *testing.T) {
		_, err := sharedEnv.Pool.Exec(ctx,
			`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, entryID)
		if err == nil {
			t.Fatal("updating a ledger entry should have been rejected")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("delete is rejected", func(t *testing.T) {
		_, err := sharedEnv.Pool.Exec(ctx,
			`DELETE FROM wallet_ledger_entries WHERE id = $1`, entryID)
		if err == nil {
			t.Fatal("deleting a ledger entry should have been rejected")
		}
		if !strings.Contains(err.Error(), "append-only") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestDatabaseRejectsNegativeBalance(t *testing.T) {
	wallet := openWallet(t, "100.00")

	_, err := sharedEnv.Pool.Exec(context.Background(),
		`UPDATE wallets SET balance_minor = -1 WHERE id = $1`, wallet.ID())
	if err == nil {
		t.Fatal("a negative balance should have been rejected by the database")
	}
	if !strings.Contains(err.Error(), "wallets_balance_non_negative") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDatabaseRejectsInconsistentLedgerMath(t *testing.T) {
	wallet := openWallet(t, "100.00")
	ctx := context.Background()

	var transactionID uuid.UUID
	err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT id FROM wager_transactions WHERE wallet_id = $1 LIMIT 1`, wallet.ID()).Scan(&transactionID)
	if err != nil {
		t.Fatalf("reading transaction failed: %v", err)
	}

	_, err = sharedEnv.Pool.Exec(ctx, `
		INSERT INTO wallet_ledger_entries
			(id, wallet_id, transaction_id, direction, amount_minor, currency,
			 balance_before_minor, balance_after_minor, created_at)
		VALUES ($1, $2, $3, 'DEBIT', 500, 'BRL', 10000, 9999, now())`,
		uuid.New(), wallet.ID(), transactionID)

	if err == nil {
		t.Fatal("a ledger entry with wrong arithmetic should have been rejected")
	}
	if !strings.Contains(err.Error(), "ledger_balance_math") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOnlyOneOpeningPerWallet(t *testing.T) {
	wallet := openWallet(t, "100.00")

	_, err := sharedEnv.Pool.Exec(context.Background(), `
		INSERT INTO wager_transactions
			(id, wallet_id, player_id, kind, amount_minor, currency, state, created_at, updated_at)
		VALUES ($1, $2, $3, 'OPENING', 5000, 'BRL', 'PROCESSED', now(), now())`,
		uuid.New(), wallet.ID(), wallet.PlayerID())

	if err == nil {
		t.Fatal("a second OPENING should have been rejected")
	}
	if !strings.Contains(err.Error(), "single_opening_per_wallet") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDuplicateWalletForPlayerAndCurrencyIsRejected(t *testing.T) {
	wallet := openWallet(t, "100.00")

	_, err := sharedEnv.Pool.Exec(context.Background(), `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, 'BRL', 0, 1, now(), now())`,
		uuid.New(), wallet.PlayerID())

	if err == nil {
		t.Fatal("a second wallet for the same player and currency should have been rejected")
	}
	if !strings.Contains(err.Error(), "wallets_player_currency_unique") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestOpeningWithZeroBalanceCreatesNoLedger(t *testing.T) {
	wallet := openWallet(t, "0.00")
	ctx := context.Background()

	var ledgerEntries, transactions, events int

	if err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, wallet.ID()).Scan(&ledgerEntries); err != nil {
		t.Fatal(err)
	}
	if err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM wager_transactions WHERE wallet_id = $1`, wallet.ID()).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = $1`, wallet.ID()).Scan(&events); err != nil {
		t.Fatal(err)
	}

	if ledgerEntries != 0 {
		t.Errorf("ledger entries = %d, want 0 for a zero balance opening", ledgerEntries)
	}
	if transactions != 0 {
		t.Errorf("transactions = %d, want 0 for a zero balance opening", transactions)
	}
	if events != 0 {
		t.Errorf("events = %d, want 0 for a zero balance opening", events)
	}
	if wallet.Version() != 1 {
		t.Errorf("version = %d, want 1", wallet.Version())
	}
}

func TestOpeningWithPositiveBalanceCreatesLedgerAndEvents(t *testing.T) {
	wallet := openWallet(t, "1000.00")
	ctx := context.Background()

	var kind, state string
	err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT kind, state FROM wager_transactions WHERE wallet_id = $1`, wallet.ID()).Scan(&kind, &state)
	if err != nil {
		t.Fatalf("reading opening transaction failed: %v", err)
	}
	if kind != "OPENING" || state != "PROCESSED" {
		t.Errorf("opening transaction = %s/%s, want OPENING/PROCESSED", kind, state)
	}

	if got := ledgerCount(t, wallet.ID(), domain.DirectionCredit); got != 1 {
		t.Errorf("credit entries = %d, want 1", got)
	}
	if wallet.Version() != 1 {
		t.Errorf("version = %d, want 1 on opening", wallet.Version())
	}

	var events int
	if err := sharedEnv.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events
		  WHERE event_type IN ('WagerTransactionProcessed', 'WalletBalanceChanged')
		    AND (aggregate_id = $1 OR aggregate_id IN
		         (SELECT id FROM wager_transactions WHERE wallet_id = $1))`,
		wallet.ID()).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("events = %d, want 2 (Processed + BalanceChanged)", events)
	}

	assertReconciled(t, wallet.ID())
}
