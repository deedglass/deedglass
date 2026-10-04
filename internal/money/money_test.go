package money

import (
	"math"
	"testing"
)

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		amount int64
		cur    string
		want   string
	}{
		{12000, "usd", "$120.00"},
		{14900, "usd", "$149.00"},
		{2000, "USD", "$20.00"},
		{5000, "jpy", "¥5,000"},
		{950, "eur", "€9.50"},
		{149900, "inr", "₹1,499.00"},
		{1250, "kwd", "KWD 1.250"},
		{1200, "chf", "CHF 12.00"},
		{5, "usd", "$0.05"},
		{0, "usd", "$0.00"},
		{100000000, "usd", "$1,000,000.00"},
		{-2000, "usd", "-$20.00"},
		{7, "bhd", "BHD 0.007"},
		{1000, "mga", "MGA 1,000"},
		{1000, "", "10.00"},
		{1000, "u$d", "10.00"},
		{math.MinInt64, "usd", "-$92,233,720,368,547,758.08"},
	} {
		if got := Format(c.amount, c.cur); got != c.want {
			t.Errorf("Format(%d, %q) = %q; want %q", c.amount, c.cur, got, c.want)
		}
	}
}

func TestExponent(t *testing.T) {
	for cur, want := range map[string]int{"usd": 2, "jpy": 0, "kwd": 3, "KRW": 0, "isk": 2, "xyz": 2} {
		if got := Exponent(cur); got != want {
			t.Errorf("Exponent(%s) = %d; want %d", cur, got, want)
		}
	}
}
