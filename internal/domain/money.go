package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const Scale = 2

const minorUnitsPerUnit = 100

type Money struct {
	minorUnits int64
	currency   string
}

func FromMinorUnits(minorUnits int64, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{minorUnits: minorUnits, currency: cur}, nil
}

func ZeroMoney(currency string) (Money, error) {
	return FromMinorUnits(0, currency)
}

func (m Money) MinorUnits() int64   { return m.minorUnits }
func (m Money) Currency() string    { return m.currency }
func (m Money) IsZero() bool        { return m.minorUnits == 0 }
func (m Money) IsPositive() bool    { return m.minorUnits > 0 }
func (m Money) IsNegative() bool    { return m.minorUnits < 0 }
func (m Money) IsInitialized() bool { return m.currency != "" }

func normalizeCurrency(currency string) (string, error) {
	if len(currency) != 3 {
		return "", ErrInvalidCurrency
	}

	upper := ""
	for i := 0; i < len(currency); i++ {
		c := currency[i]
		if c >= 'a' && c <= 'z' {
			c = c - 'a' + 'A'
		}
		if c < 'A' || c > 'Z' {
			return "", ErrInvalidCurrency
		}
		upper += string(c)
	}
	return upper, nil
}

func parseDecimalToMinorUnits(amount string) (int64, error) {
	if amount == "" {
		return 0, ErrInvalidMoneyFormat
	}

	body := amount
	negative := false
	if body[0] == '-' {
		negative = true
		body = body[1:]
	}

	intPart, fracPart, hasDot := strings.Cut(body, ".")

	if intPart == "" || (hasDot && fracPart == "") {
		return 0, ErrInvalidMoneyFormat
	}

	if len(fracPart) > Scale {
		return 0, ErrInvalidMoneyFormat
	}

	if !isASCIIDigits(intPart) || (hasDot && !isASCIIDigits(fracPart)) {
		return 0, ErrInvalidMoneyFormat
	}

	units, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		if errors.Is(err, strconv.ErrRange) {
			return 0, ErrMoneyOverflow
		}
		return 0, ErrInvalidMoneyFormat
	}

	frac := int64(0)
	if hasDot {
		padded := fracPart + strings.Repeat("0", Scale-len(fracPart))
		frac, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, ErrInvalidMoneyFormat
		}
	}

	if units > (math.MaxInt64-frac)/minorUnitsPerUnit {
		return 0, ErrMoneyOverflow
	}

	total := units*minorUnitsPerUnit + frac
	if negative {
		total = -total
	}

	return total, nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func ParseMoney(amount, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}

	units, err := parseDecimalToMinorUnits(amount)
	if err != nil {
		return Money{}, err
	}

	if units < 0 {
		return Money{}, ErrNegativeAmount
	}

	return Money{minorUnits: units, currency: cur}, nil
}

func (m Money) String() string {
	units := m.minorUnits / minorUnitsPerUnit
	frac := m.minorUnits % minorUnitsPerUnit

	sign := ""
	if m.minorUnits < 0 {
		sign = "-"
		units = -units
		frac = -frac
	}

	return fmt.Sprintf("%s%d.%0*d", sign, units, Scale, frac)
}

func (m Money) assertSameCurrency(other Money) error {
	if !m.IsInitialized() || !other.IsInitialized() {
		return ErrUninitializedMoney
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.assertSameCurrency(other); err != nil {
		return Money{}, err
	}

	sum := m.minorUnits + other.minorUnits
	if (m.minorUnits > 0 && other.minorUnits > 0 && sum < 0) || (m.minorUnits < 0 && other.minorUnits < 0 && sum > 0) {
		return Money{}, ErrMoneyOverflow
	}

	return Money{minorUnits: sum, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.assertSameCurrency(other); err != nil {
		return Money{}, err
	}

	diff := m.minorUnits - other.minorUnits
	if (m.minorUnits >= 0 && other.minorUnits < 0 && diff < 0) || (m.minorUnits < 0 && other.minorUnits > 0 && diff > 0) {
		return Money{}, ErrMoneyOverflow
	}

	return Money{minorUnits: diff, currency: m.currency}, nil
}

func (m Money) Neg() (Money, error) {
	if m.minorUnits == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

func (m Money) Cmp(other Money) (int, error) {
	if err := m.assertSameCurrency(other); err != nil {
		return 0, err
	}

	switch {
	case m.minorUnits < other.minorUnits:
		return -1, nil
	case m.minorUnits > other.minorUnits:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) bool {
	return m.minorUnits == other.minorUnits && m.currency == other.currency
}

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	return json.Marshal(moneyDTO{Amount: m.String(), Currency: m.currency})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var dto moneyDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		return ErrInvalidMoneyFormat
	}

	parsed, err := ParseMoney(dto.Amount, dto.Currency)
	if err != nil {
		return err
	}

	*m = parsed
	return nil
}
