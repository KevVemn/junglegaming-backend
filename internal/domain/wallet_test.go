package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testWallet(t *testing.T, initial string) *Wallet {
	t.Helper()
	balance := mustParse(t, initial, "BRL")
	w, err := NewWallet(uuid.New(), uuid.New(), balance, time.Now())
	if err != nil {
		t.Fatalf("NewWallet failed: %v", err)
	}
	return w
}

func TestNewWalletStartsAtVersionOne(t *testing.T) {
	w := testWallet(t, "1000.00")

	if w.Version() != 1 {
		t.Errorf("version = %d, want 1", w.Version())
	}
	if w.Balance().String() != "1000.00" {
		t.Errorf("balance = %s, want 1000.00", w.Balance())
	}
	if w.Currency() != "BRL" {
		t.Errorf("currency = %q, want BRL", w.Currency())
	}
}

func TestNewWalletRejectsInvalidInput(t *testing.T) {
	valid := mustParse(t, "100.00", "BRL")
	negative, _ := FromMinorUnits(-100, "BRL")
	now := time.Now()

	cases := []struct {
		name     string
		id       uuid.UUID
		playerID uuid.UUID
		balance  Money
		wantErr  error
	}{
		{"nil wallet id", uuid.Nil, uuid.New(), valid, ErrInvalidWalletID},
		{"nil player id", uuid.New(), uuid.Nil, valid, ErrInvalidPlayerID},
		{"uninitialized money", uuid.New(), uuid.New(), Money{}, ErrUninitializedMoney},
		{"negative balance", uuid.New(), uuid.New(), negative, ErrNegativeAmount},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewWallet(c.id, c.playerID, c.balance, now)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestWalletDebitReducesBalanceAndBumpsVersion(t *testing.T) {
	w := testWallet(t, "100.00")
	amount := mustParse(t, "25.00", "BRL")
	txID := uuid.New()

	entry, err := w.Debit(amount, txID, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Balance().String() != "75.00" {
		t.Errorf("balance = %s, want 75.00", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
	if entry.Direction() != DirectionDebit {
		t.Errorf("direction = %s, want DEBIT", entry.Direction())
	}
	if entry.BalanceBefore().String() != "100.00" || entry.BalanceAfter().String() != "75.00" {
		t.Errorf("ledger entry = %s -> %s, want 100.00 -> 75.00", entry.BalanceBefore(), entry.BalanceAfter())
	}
	if entry.TransactionID() != txID {
		t.Error("ledger entry does not reference the transaction")
	}
}

func TestWalletDebitRejectsInsufficientFunds(t *testing.T) {
	w := testWallet(t, "100.00")
	amount := mustParse(t, "100.01", "BRL")

	_, err := w.Debit(amount, uuid.New(), time.Now())
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("error = %v, want ErrInsufficientFunds", err)
	}

	if w.Balance().String() != "100.00" {
		t.Errorf("balance changed on rejected debit: %s", w.Balance())
	}
	if w.Version() != 1 {
		t.Errorf("version changed on rejected debit: %d", w.Version())
	}
}

func TestWalletDebitExactBalanceIsAllowed(t *testing.T) {
	w := testWallet(t, "100.00")
	amount := mustParse(t, "100.00", "BRL")

	if _, err := w.Debit(amount, uuid.New(), time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Errorf("balance = %s, want 0.00", w.Balance())
	}
}

func TestWalletCreditIncreasesBalance(t *testing.T) {
	w := testWallet(t, "100.00")
	amount := mustParse(t, "50.00", "BRL")

	entry, err := w.Credit(amount, uuid.New(), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.Balance().String() != "150.00" {
		t.Errorf("balance = %s, want 150.00", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
	if entry.Direction() != DirectionCredit {
		t.Errorf("direction = %s, want CREDIT", entry.Direction())
	}
}

func TestWalletRejectsCurrencyMismatch(t *testing.T) {
	w := testWallet(t, "100.00")
	usd := mustParse(t, "10.00", "USD")

	if _, err := w.Debit(usd, uuid.New(), time.Now()); !errors.Is(err, ErrWalletCurrencyKind) {
		t.Errorf("debit error = %v, want ErrWalletCurrencyKind", err)
	}
	if _, err := w.Credit(usd, uuid.New(), time.Now()); !errors.Is(err, ErrWalletCurrencyKind) {
		t.Errorf("credit error = %v, want ErrWalletCurrencyKind", err)
	}
}

func TestWalletRejectsNonPositiveAmounts(t *testing.T) {
	w := testWallet(t, "100.00")
	zero := mustParse(t, "0.00", "BRL")

	if _, err := w.Debit(zero, uuid.New(), time.Now()); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("debit error = %v, want ErrNonPositiveAmount", err)
	}
	if _, err := w.Credit(zero, uuid.New(), time.Now()); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("credit error = %v, want ErrNonPositiveAmount", err)
	}
}

func TestWalletCanDebit(t *testing.T) {
	w := testWallet(t, "100.00")

	cases := []struct {
		amount string
		want   bool
	}{
		{"50.00", true},
		{"100.00", true},
		{"100.01", false},
		{"999.99", false},
	}

	for _, c := range cases {
		t.Run(c.amount, func(t *testing.T) {
			amount := mustParse(t, c.amount, "BRL")
			got, err := w.CanDebit(amount)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("CanDebit(%s) = %v, want %v", c.amount, got, c.want)
			}
		})
	}

	if w.Version() != 1 {
		t.Error("CanDebit must not mutate the wallet")
	}
}

func TestRehydrateWalletPreservesState(t *testing.T) {
	id, playerID := uuid.New(), uuid.New()
	balance := mustParse(t, "250.75", "BRL")
	created := time.Now().Add(-time.Hour)
	updated := time.Now()

	w, err := RehydrateWallet(id, playerID, balance, 7, created, updated)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if w.ID() != id || w.PlayerID() != playerID {
		t.Error("identity not preserved")
	}
	if w.Balance().String() != "250.75" {
		t.Errorf("balance = %s, want 250.75", w.Balance())
	}
	if w.Version() != 7 {
		t.Errorf("version = %d, want 7 (rehydration must not bump it)", w.Version())
	}
}

func TestRehydrateWalletRejectsInvalidVersion(t *testing.T) {
	balance := mustParse(t, "10.00", "BRL")
	now := time.Now()

	if _, err := RehydrateWallet(uuid.New(), uuid.New(), balance, 0, now, now); err == nil {
		t.Error("version 0 should be rejected")
	}
}

func TestWalletSequentialOperationsKeepLedgerConsistent(t *testing.T) {
	w := testWallet(t, "100.00")
	now := time.Now()

	entries := make([]*WalletLedgerEntry, 0, 3)

	e1, err := w.Debit(mustParse(t, "30.00", "BRL"), uuid.New(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries = append(entries, e1)

	e2, err := w.Credit(mustParse(t, "10.00", "BRL"), uuid.New(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries = append(entries, e2)

	e3, err := w.Debit(mustParse(t, "5.00", "BRL"), uuid.New(), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries = append(entries, e3)

	rebuilt := mustParse(t, "100.00", "BRL")
	for _, e := range entries {
		signed, err := e.SignedAmount()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rebuilt, err = rebuilt.Add(signed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if !rebuilt.Equal(w.Balance()) {
		t.Errorf("ledger rebuild = %s, wallet balance = %s", rebuilt, w.Balance())
	}
	if w.Version() != 4 {
		t.Errorf("version = %d, want 4", w.Version())
	}
}
