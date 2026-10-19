package domain

import (
	"time"

	"github.com/google/uuid"
)

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func NewWallet(id, playerID uuid.UUID, initialBalance Money, now time.Time) (*Wallet, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if !initialBalance.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	if initialBalance.IsNegative() {
		return nil, ErrNegativeAmount
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  initialBalance.Currency(),
		balance:   initialBalance,
		version:   1,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

func RehydrateWallet(
	id, playerID uuid.UUID,
	balance Money,
	version int64,
	createdAt, updatedAt time.Time,
) (*Wallet, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if !balance.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	if version < 1 {
		return nil, ErrInvalidTransactionState
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  balance.Currency(),
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

func (w *Wallet) ID() uuid.UUID        { return w.id }
func (w *Wallet) PlayerID() uuid.UUID  { return w.playerID }
func (w *Wallet) Currency() string     { return w.currency }
func (w *Wallet) Balance() Money       { return w.balance }
func (w *Wallet) Version() int64       { return w.version }
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

func (w *Wallet) Debit(amount Money, transactionID uuid.UUID, now time.Time) (*WalletLedgerEntry, error) {
	if err := w.assertOperable(amount); err != nil {
		return nil, err
	}
	if !amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}

	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return nil, err
	}
	if newBalance.IsNegative() {
		return nil, ErrInsufficientFunds
	}

	entry, err := NewWalletLedgerEntry(
		uuid.New(), w.id, transactionID,
		DirectionDebit, amount, w.balance, newBalance, now,
	)
	if err != nil {
		return nil, err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now.UTC()

	return entry, nil
}

func (w *Wallet) Credit(amount Money, transactionID uuid.UUID, now time.Time) (*WalletLedgerEntry, error) {
	if err := w.assertOperable(amount); err != nil {
		return nil, err
	}
	if !amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}

	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return nil, err
	}

	entry, err := NewWalletLedgerEntry(
		uuid.New(), w.id, transactionID,
		DirectionCredit, amount, w.balance, newBalance, now,
	)
	if err != nil {
		return nil, err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now.UTC()

	return entry, nil
}

func (w *Wallet) CanDebit(amount Money) (bool, error) {
	if err := w.assertOperable(amount); err != nil {
		return false, err
	}
	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return false, err
	}
	return !newBalance.IsNegative(), nil
}

func (w *Wallet) assertOperable(amount Money) error {
	if !amount.IsInitialized() {
		return ErrUninitializedMoney
	}
	if amount.Currency() != w.currency {
		return ErrWalletCurrencyKind
	}
	return nil
}
