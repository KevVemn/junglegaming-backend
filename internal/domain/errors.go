package domain

import "errors"

var (
	ErrInvalidMoneyFormat = errors.New("invalid money format")
	ErrNegativeAmount     = errors.New("negative amount is not allowed")
	ErrInvalidCurrency    = errors.New("invalid currency code")
	ErrCurrencyMismatch   = errors.New("currency mismatch")
	ErrMoneyOverflow      = errors.New("money arithmetic overflow")
	ErrUninitializedMoney = errors.New("uninitialized money value")
)

var (
	ErrInvalidWalletID    = errors.New("invalid wallet id")
	ErrInvalidPlayerID    = errors.New("invalid player id")
	ErrInsufficientFunds  = errors.New("insufficient funds")
	ErrNonPositiveAmount  = errors.New("amount must be greater than zero")
	ErrWalletCurrencyKind = errors.New("operation currency does not match wallet currency")
)

var (
	ErrInvalidLedgerDirection = errors.New("invalid ledger direction")
	ErrLedgerBalanceMismatch  = errors.New("ledger balance does not match direction and amount")
	ErrInvalidTransactionID   = errors.New("invalid transaction id")
)

var (
	ErrInvalidTransactionKind  = errors.New("invalid transaction kind")
	ErrInvalidTransactionState = errors.New("invalid transaction state")
	ErrTerminalState           = errors.New("transaction is already in a terminal state")
	ErrInvalidStateTransition  = errors.New("invalid state transition")
	ErrInternalKindNotAllowed  = errors.New("internal transaction kind is not accepted from external sources")
	ErrReferenceRequired       = errors.New("reference external transaction id is required")
	ErrReferenceNotAllowed     = errors.New("reference external transaction id is not allowed for this kind")
	ErrInvalidProviderID       = errors.New("invalid provider id")
	ErrInvalidExternalID       = errors.New("invalid external transaction id")
	ErrInvalidIdempotencyKey   = errors.New("invalid idempotency key")
)

type FailureCode string

const (
	FailureInsufficientFunds   FailureCode = "INSUFFICIENT_FUNDS"
	FailureReferenceNotFound   FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotUsable  FailureCode = "REFERENCE_NOT_USABLE"
	FailureReversalExceedsFund FailureCode = "REVERSAL_EXCEEDS_BALANCE"
	FailureAlreadyReversed     FailureCode = "ALREADY_REVERSED"
	FailureReferenceMismatch   FailureCode = "REFERENCE_MISMATCH"
	FailureInvalidAmount       FailureCode = "INVALID_AMOUNT"
	FailureCurrencyMismatch    FailureCode = "CURRENCY_MISMATCH"
	FailureWalletNotFound      FailureCode = "WALLET_NOT_FOUND"
	FailurePermanentError      FailureCode = "PERMANENT_ERROR"
)

func (f FailureCode) String() string { return string(f) }
