package policy

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in       string
		amount   int64
		currency string
		code     string // empty when the input is valid
		message  string // checked when not empty
		hint     string // checked when not empty
	}{
		{in: "50.00 usd", amount: 5000, currency: "usd"},
		{in: "50 usd", amount: 5000, currency: "usd"},
		{in: "0.5 usd", amount: 50, currency: "usd"},
		{in: "0 usd", amount: 0, currency: "usd"},
		{in: "500 jpy", amount: 500, currency: "jpy"},
		{in: "1.234 kwd", amount: 1234, currency: "kwd"},
		{in: "50.00 USD", amount: 5000, currency: "usd"},
		{in: "12.5 Inr", amount: 1250, currency: "inr"},
		{in: "90071992547409.91 usd", amount: MaxMinor, currency: "usd"},
		{in: "9007199254740991 jpy", amount: MaxMinor, currency: "jpy"},

		{in: "5.5 jpy", code: "money_precision",
			message: "`5.5 jpy` has more decimals than jpy allows (0).",
			hint:    `Write a whole number, like "6 jpy".`},
		{in: "50.005 usd", code: "money_precision",
			message: "`50.005 usd` has more decimals than usd allows (2).",
			hint:    `Round it to 2 decimals, like "50.01 usd".`},
		{in: "50.000 usd", code: "money_precision",
			hint: `Round it to 2 decimals, like "50.00 usd".`},
		{in: "9.999 usd", code: "money_precision",
			hint: `Round it to 2 decimals, like "10.00 usd".`},
		{in: "0.004 usd", code: "money_precision",
			hint: `Round it to 2 decimals, like "0.00 usd".`},

		{in: "-5.00 usd", code: "invalid_money",
			message: "`-5.00 usd` is negative.",
			hint:    "Money in a policy is never negative."},
		{in: "90071992547409.92 usd", code: "invalid_money",
			message: "`90071992547409.92 usd` is too large.",
			hint:    "The largest amount is 9,007,199,254,740,991 in minor units."},
		{in: "9007199254740992 jpy", code: "invalid_money", message: "`9007199254740992 jpy` is too large."},
		{in: "123456789012345678901234567890 usd", code: "invalid_money",
			message: "`123456789012345678901234567890 usd` is too large."},
		{in: "184467440737095516.16 usd", code: "invalid_money"}, // 2^64 in cents

		{in: "50 xyz", code: "unknown_currency",
			message: "`xyz` is not an active ISO 4217 currency.",
			hint:    "Use a three-letter code, like usd, eur or inr."},
		// "uds" is usd with two letters swapped: one Damerau edit, and usd is common.
		{in: "50 uds", code: "unknown_currency",
			hint: `Did you mean "usd"? Use a three-letter code, like usd, eur or inr.`},
		{in: "50 qqq", code: "unknown_currency",
			hint: "Use a three-letter code, like usd, eur or inr."},
		{in: "50 usb", code: "unknown_currency",
			hint: `Did you mean "usd"? Use a three-letter code, like usd, eur or inr.`},
		// usx is one edit from both ugx and usd; the common code wins.
		{in: "50 USX", code: "unknown_currency",
			message: "`USX` is not an active ISO 4217 currency.",
			hint:    `Did you mean "usd"? Use a three-letter code, like usd, eur or inr.`},
		{in: "50 XAU", code: "unknown_currency", message: "`XAU` is not an active ISO 4217 currency."},

		{in: "50 usdd", code: "invalid_money",
			message: "`50 usdd` is not an amount of money.",
			hint:    `Write money as a string with a currency, like "50.00 usd".`},
		{in: "50usd", code: "invalid_money"},
		{in: " 50 usd", code: "invalid_money"},
		{in: "50 usd ", code: "invalid_money"},
		{in: "50  usd", code: "invalid_money"},
		{in: "fifty usd", code: "invalid_money"},
		{in: "5e3 usd", code: "invalid_money"},
		{in: "1,000.00 usd", code: "invalid_money"},
		{in: "50. usd", code: "invalid_money"},
		{in: ".5 usd", code: "invalid_money"},
		{in: "+5 usd", code: "invalid_money"},
		{in: "50 us1", code: "invalid_money"},
		{in: "", code: "invalid_money"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			m, p := ParseMoney(tc.in)
			if tc.code == "" {
				if p != nil {
					t.Fatalf("ParseMoney(%q) refused: %+v", tc.in, *p)
				}
				if m.Amount != tc.amount || m.Currency != tc.currency {
					t.Fatalf("ParseMoney(%q) = %+v, want {%d %s}", tc.in, m, tc.amount, tc.currency)
				}
				back, p2 := ParseMoney(m.String())
				if p2 != nil || back != m {
					t.Fatalf("round trip of %q via %q = %+v, %v", tc.in, m.String(), back, p2)
				}
				return
			}
			if p == nil {
				t.Fatalf("ParseMoney(%q) = %+v, want %s", tc.in, m, tc.code)
			}
			if p.Code != tc.code {
				t.Errorf("code = %q, want %q (%s)", p.Code, tc.code, p.Message)
			}
			if tc.message != "" && p.Message != tc.message {
				t.Errorf("message = %q, want %q", p.Message, tc.message)
			}
			if tc.hint != "" && p.Hint != tc.hint {
				t.Errorf("hint = %q, want %q", p.Hint, tc.hint)
			}
			checkTone(t, p)
		})
	}
}

func checkTone(t *testing.T, p *moneyProblem) {
	t.Helper()
	if !strings.HasSuffix(p.Message, ".") {
		t.Errorf("message %q does not end with a period", p.Message)
	}
	if !strings.HasSuffix(p.Hint, ".") {
		t.Errorf("hint %q does not end with a period", p.Hint)
	}
	for _, s := range []string{p.Message, p.Hint} {
		if strings.ContainsAny(s, "!—") {
			t.Errorf("%q has an exclamation mark or em dash", s)
		}
	}
}

func TestMoneyString(t *testing.T) {
	tests := []struct {
		m    Money
		want string
	}{
		{Money{5000, "usd"}, "50.00 usd"},
		{Money{5, "usd"}, "0.05 usd"},
		{Money{0, "usd"}, "0.00 usd"},
		{Money{500, "jpy"}, "500 jpy"},
		{Money{1234, "kwd"}, "1.234 kwd"},
		{Money{1, "clf"}, "0.0001 clf"},
		{Money{MaxMinor, "usd"}, "90071992547409.91 usd"},
	}
	for _, tc := range tests {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.m, got, tc.want)
		}
	}
}

func TestParseMoneyClipsLongInput(t *testing.T) {
	in := strings.Repeat("9", 100) + " usd"
	_, p := ParseMoney(in)
	if p == nil {
		t.Fatal("want a problem")
	}
	quoted := strings.TrimSuffix(strings.TrimPrefix(p.Message, "`"), "` is too large.")
	if utf8.RuneCountInString(quoted) != moneyQuoteLimit+1 || !strings.HasSuffix(quoted, "…") {
		t.Errorf("quoted text %q is not clipped to %d runes plus an ellipsis", quoted, moneyQuoteLimit)
	}
}

func TestCurrencyTable(t *testing.T) {
	for code, exp := range currencyExponents {
		if len(code) != 3 || strings.ToLower(code) != code || !moneyLetters(code) {
			t.Errorf("code %q is not three lowercase letters", code)
		}
		if exp < 0 || exp > 4 {
			t.Errorf("%s has exponent %d, want 0..4", code, exp)
		}
	}
	for code, want := range map[string]int{"usd": 2, "eur": 2, "inr": 2, "jpy": 0, "kwd": 3, "gbp": 2} {
		got, ok := CurrencyExponent(code)
		if !ok || got != want {
			t.Errorf("CurrencyExponent(%q) = %d, %v, want %d, true", code, got, ok, want)
		}
	}
	for _, code := range []string{"xau", "xdr", "xxx", "xts", "USD"} {
		if _, ok := CurrencyExponent(code); ok {
			t.Errorf("CurrencyExponent(%q) is known, want unknown", code)
		}
	}
}
