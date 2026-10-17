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
