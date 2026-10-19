package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustParse(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := ParseMoney(amount, currency)
	if err != nil {
		t.Fatalf("ParseMoney(%q, %q) failed: %v", amount, currency, err)
	}
	return m
}

func TestParseMoneyValidInputs(t *testing.T) {
	cases := []struct {
		name   string
		amount string
		want   int64
	}{
		{"two decimal places", "25.00", 2500},
		{"zero with decimals", "0.00", 0},
		{"zero without decimals", "0", 0},
		{"integer without dot", "25", 2500},
		{"single decimal means tens", "25.5", 2550},
		{"cents only", "0.07", 7},
		{"large value", "1000000.99", 100000099},
		{"leading zeros accepted", "025.00", 2500},
		{"max representable", "92233720368547758.07", math.MaxInt64},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := ParseMoney(c.amount, "BRL")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.MinorUnits() != c.want {
				t.Errorf("ParseMoney(%q) = %d minor units, want %d", c.amount, m.MinorUnits(), c.want)
			}
			if m.Currency() != "BRL" {
				t.Errorf("currency = %q, want BRL", m.Currency())
			}
		})
	}
}

func TestParseMoneyRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name    string
		amount  string
		wantErr error
	}{
		{"empty string", "", ErrInvalidMoneyFormat},
		{"NaN", "NaN", ErrInvalidMoneyFormat},
		{"Infinity", "Infinity", ErrInvalidMoneyFormat},
		{"Inf", "Inf", ErrInvalidMoneyFormat},
		{"scientific lowercase", "1e2", ErrInvalidMoneyFormat},
		{"scientific uppercase", "1E2", ErrInvalidMoneyFormat},
		{"scientific signed", "2.5e+10", ErrInvalidMoneyFormat},
		{"scale exceeded", "25.001", ErrInvalidMoneyFormat},
		{"scale far exceeded", "0.123456", ErrInvalidMoneyFormat},
		{"dot without fraction", "25.", ErrInvalidMoneyFormat},
		{"fraction without integer", ".50", ErrInvalidMoneyFormat},
		{"leading space", " 25.00", ErrInvalidMoneyFormat},
		{"trailing space", "25.00 ", ErrInvalidMoneyFormat},
		{"comma separator", "25,00", ErrInvalidMoneyFormat},
		{"currency symbol", "R$ 25.00", ErrInvalidMoneyFormat},
		{"explicit plus sign", "+25.00", ErrInvalidMoneyFormat},
		{"letters", "abc", ErrInvalidMoneyFormat},
		{"two dots", "25.00.00", ErrInvalidMoneyFormat},
		{"hexadecimal", "0x19", ErrInvalidMoneyFormat},
		{"negative value", "-5.00", ErrNegativeAmount},
		{"negative cents", "-0.01", ErrNegativeAmount},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseMoney(c.amount, "BRL")
			if !errors.Is(err, c.wantErr) {
				t.Errorf("ParseMoney(%q) error = %v, want %v", c.amount, err, c.wantErr)
			}
		})
	}
}

func TestParseMoneyRejectsInvalidCurrency(t *testing.T) {
	for _, currency := range []string{"", "B", "BR", "BRLL", "BR1", "BR$", "123", "br l"} {
		t.Run("currency_"+currency, func(t *testing.T) {
			if _, err := ParseMoney("25.00", currency); !errors.Is(err, ErrInvalidCurrency) {
				t.Errorf("ParseMoney with currency %q error = %v, want ErrInvalidCurrency", currency, err)
			}
		})
	}
}

func TestParseMoneyNormalizesLowercaseCurrency(t *testing.T) {
	m := mustParse(t, "10.00", "brl")
	if m.Currency() != "BRL" {
		t.Errorf("currency = %q, want BRL", m.Currency())
	}
}

func TestParseMoneyDetectsOverflow(t *testing.T) {
	for _, amount := range []string{"92233720368547758.08", "99999999999999999999.00"} {
		t.Run(amount, func(t *testing.T) {
			if _, err := ParseMoney(amount, "BRL"); !errors.Is(err, ErrMoneyOverflow) {
				t.Errorf("ParseMoney(%q) error = %v, want ErrMoneyOverflow", amount, err)
			}
		})
	}
}

func TestMoneyString(t *testing.T) {
	cases := []struct {
		minorUnits int64
		want       string
	}{
		{2500, "25.00"},
		{0, "0.00"},
		{7, "0.07"},
		{70, "0.70"},
		{-50, "-0.50"},
		{-2500, "-25.00"},
		{-7, "-0.07"},
		{100000099, "1000000.99"},
		{math.MaxInt64, "92233720368547758.07"},
		{math.MinInt64, "-92233720368547758.08"},
	}

	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			m, err := FromMinorUnits(c.minorUnits, "BRL")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := m.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMoneyRoundTrip(t *testing.T) {
	for _, value := range []string{"0.00", "0.01", "25.00", "999999.99", "92233720368547758.07"} {
		t.Run(value, func(t *testing.T) {
			m := mustParse(t, value, "BRL")
			if got := m.String(); got != value {
				t.Errorf("round trip of %q produced %q", value, got)
			}
		})
	}
}

func TestMoneyEquivalentFormsProduceSameValue(t *testing.T) {
	cases := []struct {
		input string
		want  int64
	}{
		{"25", 2500},
		{"25.5", 2550},
		{"025.50", 2550},
	}

	for _, c := range cases {
		m := mustParse(t, c.input, "BRL")
		if m.MinorUnits() != c.want {
			t.Errorf("ParseMoney(%q) = %d, want %d", c.input, m.MinorUnits(), c.want)
		}
	}
}

func TestMoneyAdd(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.50", "BRL")

	sum, err := a.Add(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sum.String() != "35.50" {
		t.Errorf("25.00 + 10.50 = %s, want 35.50", sum)
	}

	if a.String() != "25.00" || b.String() != "10.50" {
		t.Error("Add mutated its operands; Money must be immutable")
	}
}

func TestMoneyAddOverflow(t *testing.T) {
	max, _ := FromMinorUnits(math.MaxInt64, "BRL")
	one, _ := FromMinorUnits(1, "BRL")
	if _, err := max.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("error = %v, want ErrMoneyOverflow", err)
	}

	min, _ := FromMinorUnits(math.MinInt64, "BRL")
	minusOne, _ := FromMinorUnits(-1, "BRL")
	if _, err := min.Add(minusOne); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("lower bound error = %v, want ErrMoneyOverflow", err)
	}
}

func TestMoneySub(t *testing.T) {
	a := mustParse(t, "25.00", "BRL")
	b := mustParse(t, "10.50", "BRL")

	diff, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff.String() != "14.50" {
		t.Errorf("25.00 - 10.50 = %s, want 14.50", diff)
	}
}

func TestMoneySubAllowsNegativeResult(t *testing.T) {
	a := mustParse(t, "10.00", "BRL")
	b := mustParse(t, "25.00", "BRL")

	diff, err := a.Sub(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff.String() != "-15.00" {
		t.Errorf("10.00 - 25.00 = %s, want -15.00", diff)
	}
	if !diff.IsNegative() {
		t.Error("IsNegative() = false, want true")
	}
}

func TestMoneySubOverflow(t *testing.T) {
	min, _ := FromMinorUnits(math.MinInt64, "BRL")
	one, _ := FromMinorUnits(1, "BRL")
	if _, err := min.Sub(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("error = %v, want ErrMoneyOverflow", err)
	}

	zero, _ := FromMinorUnits(0, "BRL")
	if _, err := zero.Sub(min); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("0 - MinInt64 error = %v, want ErrMoneyOverflow", err)
	}
}

func TestMoneyNeg(t *testing.T) {
	m := mustParse(t, "25.00", "BRL")

	negated, err := m.Neg()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if negated.String() != "-25.00" {
		t.Errorf("Neg() = %s, want -25.00", negated)
	}

	back, err := negated.Neg()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !back.Equal(m) {
		t.Errorf("double negation = %s, want %s", back, m)
	}
}

func TestMoneyNegOverflowAtMinInt64(t *testing.T) {
	min, _ := FromMinorUnits(math.MinInt64, "BRL")
	if _, err := min.Neg(); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("error = %v, want ErrMoneyOverflow", err)
	}
}

func TestMoneyCurrencyMismatch(t *testing.T) {
	brl := mustParse(t, "25.00", "BRL")
	usd := mustParse(t, "25.00", "USD")

	t.Run("Add", func(t *testing.T) {
		if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Errorf("error = %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("Sub", func(t *testing.T) {
		if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Errorf("error = %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("Cmp", func(t *testing.T) {
		if _, err := brl.Cmp(usd); !errors.Is(err, ErrCurrencyMismatch) {
			t.Errorf("error = %v, want ErrCurrencyMismatch", err)
		}
	})

	t.Run("Equal returns false instead of erroring", func(t *testing.T) {
		if brl.Equal(usd) {
			t.Error("values in different currencies must not be equal")
		}
	})
}

func TestMoneyCmp(t *testing.T) {
	lower := mustParse(t, "10.00", "BRL")
	higher := mustParse(t, "25.00", "BRL")
	same := mustParse(t, "10.00", "BRL")

	cases := []struct {
		name string
		a, b Money
		want int
	}{
		{"less than", lower, higher, -1},
		{"greater than", higher, lower, 1},
		{"equal", lower, same, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.a.Cmp(c.b)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("Cmp() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestMoneyZeroValueIsRejected(t *testing.T) {
	var empty Money
	valid := mustParse(t, "25.00", "BRL")

	if empty.IsInitialized() {
		t.Error("zero value Money must not report as initialized")
	}

	if _, err := empty.Add(valid); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("error = %v, want ErrUninitializedMoney", err)
	}

	if _, err := valid.Add(empty); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("operand error = %v, want ErrUninitializedMoney", err)
	}

	if _, err := empty.MarshalJSON(); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("marshal error = %v, want ErrUninitializedMoney", err)
	}
}

func TestMoneyPredicates(t *testing.T) {
	zero := mustParse(t, "0.00", "BRL")
	positive := mustParse(t, "25.00", "BRL")
	negative, _ := FromMinorUnits(-2500, "BRL")

	if !zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Error("wrong predicates for zero")
	}
	if positive.IsZero() || !positive.IsPositive() || positive.IsNegative() {
		t.Error("wrong predicates for positive value")
	}
	if negative.IsZero() || negative.IsPositive() || !negative.IsNegative() {
		t.Error("wrong predicates for negative value")
	}
}

func TestZeroMoney(t *testing.T) {
	z, err := ZeroMoney("BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !z.IsZero() || z.String() != "0.00" || z.Currency() != "BRL" {
		t.Errorf("ZeroMoney(BRL) = %s %s", z, z.Currency())
	}
}

func TestFromMinorUnitsAcceptsNegative(t *testing.T) {
	m, err := FromMinorUnits(-1500, "BRL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.String() != "-15.00" {
		t.Errorf("String() = %s, want -15.00", m)
	}
}

func TestMoneyMarshalJSON(t *testing.T) {
	m := mustParse(t, "25.00", "BRL")

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"amount":"25.00","currency":"BRL"}`
	if string(data) != want {
		t.Errorf("JSON = %s, want %s", data, want)
	}
}

func TestMoneyUnmarshalJSON(t *testing.T) {
	var m Money
	if err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.MinorUnits() != 2500 || m.Currency() != "BRL" {
		t.Errorf("decoded = %d %s, want 2500 BRL", m.MinorUnits(), m.Currency())
	}
}

func TestMoneyUnmarshalRejectsNumericAmount(t *testing.T) {
	var m Money
	err := json.Unmarshal([]byte(`{"amount":25.00,"currency":"BRL"}`), &m)
	if !errors.Is(err, ErrInvalidMoneyFormat) {
		t.Errorf("error = %v, want ErrInvalidMoneyFormat", err)
	}
}

func TestMoneyUnmarshalRejectsInvalidValues(t *testing.T) {
	inputs := []string{
		`{"amount":"25.001","currency":"BRL"}`,
		`{"amount":"-5.00","currency":"BRL"}`,
		`{"amount":"1e2","currency":"BRL"}`,
		`{"amount":"","currency":"BRL"}`,
		`{"amount":"25.00","currency":"BR"}`,
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			var m Money
			if err := json.Unmarshal([]byte(input), &m); err == nil {
				t.Errorf("%s should have been rejected", input)
			}
		})
	}
}

func TestMoneyJSONRoundTrip(t *testing.T) {
	original := mustParse(t, "1234.56", "BRL")

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded Money
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !decoded.Equal(original) {
		t.Errorf("JSON round trip: %s != %s", decoded, original)
	}
}
