// Package money formats integer minor units for people (S30): "$120.00", "¥5,000", "KWD 1.250".
// Amounts are always an int64 of minor units plus a lowercase ISO 4217 currency; nothing here
// parses or produces a float. The exponents follow Stripe's tables, which agree with the ISO
// table the policy language's minor() reads for every currency the starters use (usd, eur, gbp,
// inr, jpy); Stripe's documented exceptions are listed below.
package money

import (
	"strconv"
	"strings"
)

// ZeroDecimal are the currencies Stripe charges in whole units (verified 2026-10-04 against
// https://docs.stripe.com/currencies#zero-decimal). mga is two-decimal in ISO 4217; Stripe
// treats it as zero-decimal.
var ZeroDecimal = []string{"bif", "clp", "djf", "gnf", "jpy", "kmf", "krw", "mga", "pyg", "rwf", "ugx", "vnd", "vuv", "xaf", "xof", "xpf"}

// ThreeDecimal are the currencies Stripe charges in thousandths
// (https://docs.stripe.com/currencies#three-decimal).
var ThreeDecimal = []string{"bhd", "jod", "kwd", "omr", "tnd"}

var symbols = map[string]string{"usd": "$", "eur": "€", "gbp": "£", "inr": "₹", "jpy": "¥"}

// Exponent is how many minor digits currency has at Stripe: 0, 3, or 2 for every other code.
func Exponent(currency string) int {
	c := strings.ToLower(currency)
	for _, z := range ZeroDecimal {
		if c == z {
			return 0
		}
	}
	for _, t := range ThreeDecimal {
		if c == t {
			return 3
		}
	}
	return 2
}

// Format writes amount minor units of currency the way people read it: a symbol for usd, eur,
// gbp, inr and jpy ("$120.00", "€9.50", "₹1,499.00", "¥5,000"), the upper-case code otherwise
// ("KWD 1.250", "CHF 12.00"). An empty or malformed currency gives the bare number.
func Format(amount int64, currency string) string {
	cur := strings.ToLower(strings.TrimSpace(currency))
	exp := Exponent(cur)
	neg := amount < 0
	u := uint64(amount)
	if neg {
		u = uint64(-(amount + 1)) + 1 // safe for math.MinInt64
	}
	digits := strconv.FormatUint(u, 10)
	for len(digits) <= exp {
		digits = "0" + digits
	}
	intPart, frac := digits[:len(digits)-exp], digits[len(digits)-exp:]
	out := group(intPart)
	if exp > 0 {
		out += "." + frac
	}
	sign := ""
	if neg {
		sign = "-"
	}
	if s, ok := symbols[cur]; ok {
		return sign + s + out
	}
	if !validCode(cur) {
		return sign + out
	}
	return sign + strings.ToUpper(cur) + " " + out
}

func group(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	head := len(s) % 3
	if head > 0 {
		b.WriteString(s[:head])
	}
	for i := head; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func validCode(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c[i] < 'a' || c[i] > 'z' {
			return false
		}
	}
	return true
}
