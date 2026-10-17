package domain

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
