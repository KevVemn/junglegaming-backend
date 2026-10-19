package domain

import (
	"time"

	"github.com/google/uuid"
)

type LedgerDirection string

const (
	DirectionDebit  LedgerDirection = "DEBIT"
	DirectionCredit LedgerDirection = "CREDIT"
)

func (d LedgerDirection) Valid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

func (d LedgerDirection) String() string { return string(d) }

type WalletLedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     LedgerDirection
	amount        Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

func NewWalletLedgerEntry(
	id, walletID, transactionID uuid.UUID,
	direction LedgerDirection,
	amount, balanceBefore, balanceAfter Money,
	now time.Time,
) (*WalletLedgerEntry, error) {
	if id == uuid.Nil || walletID == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if transactionID == uuid.Nil {
		return nil, ErrInvalidTransactionID
	}
	if !direction.Valid() {
		return nil, ErrInvalidLedgerDirection
	}
	if !amount.IsInitialized() || !balanceBefore.IsInitialized() || !balanceAfter.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	if !amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}

	expected, err := applyDirection(balanceBefore, amount, direction)
	if err != nil {
		return nil, err
	}
	if !expected.Equal(balanceAfter) {
		return nil, ErrLedgerBalanceMismatch
	}
	if balanceAfter.IsNegative() {
		return nil, ErrInsufficientFunds
	}

	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     now.UTC(),
	}, nil
}

func RehydrateWalletLedgerEntry(
	id, walletID, transactionID uuid.UUID,
	direction LedgerDirection,
	amount, balanceBefore, balanceAfter Money,
	createdAt time.Time,
) (*WalletLedgerEntry, error) {
	return NewWalletLedgerEntry(
		id, walletID, transactionID, direction,
		amount, balanceBefore, balanceAfter, createdAt,
	)
}

func (e *WalletLedgerEntry) ID() uuid.UUID              { return e.id }
func (e *WalletLedgerEntry) WalletID() uuid.UUID        { return e.walletID }
func (e *WalletLedgerEntry) TransactionID() uuid.UUID   { return e.transactionID }
func (e *WalletLedgerEntry) Direction() LedgerDirection { return e.direction }
func (e *WalletLedgerEntry) Amount() Money              { return e.amount }
func (e *WalletLedgerEntry) BalanceBefore() Money       { return e.balanceBefore }
func (e *WalletLedgerEntry) BalanceAfter() Money        { return e.balanceAfter }
func (e *WalletLedgerEntry) CreatedAt() time.Time       { return e.createdAt }

func (e *WalletLedgerEntry) SignedAmount() (Money, error) {
	if e.direction == DirectionCredit {
		return e.amount, nil
	}
	return e.amount.Neg()
}

func applyDirection(base, amount Money, direction LedgerDirection) (Money, error) {
	if direction == DirectionCredit {
		return base.Add(amount)
	}
	return base.Sub(amount)
}
