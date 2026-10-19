package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewWalletLedgerEntryValid(t *testing.T) {
	before := mustParse(t, "100.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")
	after := mustParse(t, "75.00", "BRL")

	entry, err := NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionDebit, amount, before, after, time.Now(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if entry.Direction() != DirectionDebit {
		t.Errorf("direction = %s, want DEBIT", entry.Direction())
	}
	if entry.Amount().String() != "25.00" {
		t.Errorf("amount = %s, want 25.00", entry.Amount())
	}
}

func TestNewWalletLedgerEntryRejectsBalanceMismatch(t *testing.T) {
	before := mustParse(t, "100.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")
	wrongAfter := mustParse(t, "80.00", "BRL")

	_, err := NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionDebit, amount, before, wrongAfter, time.Now(),
	)
	if !errors.Is(err, ErrLedgerBalanceMismatch) {
		t.Errorf("error = %v, want ErrLedgerBalanceMismatch", err)
	}
}

func TestNewWalletLedgerEntryRejectsWrongDirectionMath(t *testing.T) {
	before := mustParse(t, "100.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")
	debitResult := mustParse(t, "75.00", "BRL")

	_, err := NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionCredit, amount, before, debitResult, time.Now(),
	)
	if !errors.Is(err, ErrLedgerBalanceMismatch) {
		t.Errorf("error = %v, want ErrLedgerBalanceMismatch", err)
	}
}

func TestNewWalletLedgerEntryRejectsInvalidInput(t *testing.T) {
	before := mustParse(t, "100.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")
	after := mustParse(t, "75.00", "BRL")
	zero := mustParse(t, "0.00", "BRL")
	now := time.Now()

	cases := []struct {
		name          string
		id, walletID  uuid.UUID
		transactionID uuid.UUID
		direction     LedgerDirection
		amount        Money
		wantErr       error
	}{
		{"nil entry id", uuid.Nil, uuid.New(), uuid.New(), DirectionDebit, amount, ErrInvalidWalletID},
		{"nil wallet id", uuid.New(), uuid.Nil, uuid.New(), DirectionDebit, amount, ErrInvalidWalletID},
		{"nil transaction id", uuid.New(), uuid.New(), uuid.Nil, DirectionDebit, amount, ErrInvalidTransactionID},
		{"invalid direction", uuid.New(), uuid.New(), uuid.New(), LedgerDirection("SIDEWAYS"), amount, ErrInvalidLedgerDirection},
		{"zero amount", uuid.New(), uuid.New(), uuid.New(), DirectionDebit, zero, ErrNonPositiveAmount},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewWalletLedgerEntry(
				c.id, c.walletID, c.transactionID,
				c.direction, c.amount, before, after, now,
			)
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestNewWalletLedgerEntryRejectsNegativeResultingBalance(t *testing.T) {
	before := mustParse(t, "10.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")
	after, err := FromMinorUnits(-1500, "BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionDebit, amount, before, after, time.Now(),
	)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("error = %v, want ErrInsufficientFunds", err)
	}
}

func TestLedgerEntrySignedAmount(t *testing.T) {
	before := mustParse(t, "100.00", "BRL")
	amount := mustParse(t, "25.00", "BRL")

	debitAfter := mustParse(t, "75.00", "BRL")
	debit, err := NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionDebit, amount, before, debitAfter, time.Now(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signed, err := debit.SignedAmount()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if signed.String() != "-25.00" {
		t.Errorf("debit signed amount = %s, want -25.00", signed)
	}

	creditAfter := mustParse(t, "125.00", "BRL")
	credit, err := NewWalletLedgerEntry(
		uuid.New(), uuid.New(), uuid.New(),
		DirectionCredit, amount, before, creditAfter, time.Now(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signed, err = credit.SignedAmount()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if signed.String() != "25.00" {
		t.Errorf("credit signed amount = %s, want 25.00", signed)
	}
}

func TestLedgerDirectionValid(t *testing.T) {
	if !DirectionDebit.Valid() || !DirectionCredit.Valid() {
		t.Error("DEBIT and CREDIT must be valid")
	}
	if LedgerDirection("").Valid() || LedgerDirection("TRANSFER").Valid() {
		t.Error("unknown directions must be invalid")
	}
}
