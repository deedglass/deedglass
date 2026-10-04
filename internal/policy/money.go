package policy

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Money is an amount in minor units of an active ISO 4217 currency (lowercase), as in contract §8.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// MaxMinor is the largest amount in minor units (2^53 − 1), as for every integer in a deed.
const MaxMinor = 1<<53 - 1

// moneyProblem says why a money string was refused. Code is one of "invalid_money", "unknown_currency", "money_precision".
type moneyProblem struct {
	Code    string
	Message string // one sentence, sentence case, ends with a period, quotes the user's own text in backticks
	Hint    string // the next step, ends with a period
}

// moneyMaxDigits is the number of decimal digits in MaxMinor.
const moneyMaxDigits = 16

// moneyQuoteLimit is the most runes of user text quoted in a message.
const moneyQuoteLimit = 60

// ParseMoney parses "<decimal> <currency>", like "50.00 usd" or "500 JPY".
func ParseMoney(s string) (Money, *moneyProblem) {
	if rest, ok := strings.CutPrefix(s, "-"); ok {
		if _, _, _, shapeOK := splitMoney(rest); shapeOK {
			return Money{}, &moneyProblem{
				Code:    "invalid_money",
				Message: "`" + moneyClip(s) + "` is negative.",
				Hint:    "Money in a policy is never negative.",
			}
		}
		return Money{}, moneyNotMoney(s)
	}

	intPart, fracPart, written, ok := splitMoney(s)
	if !ok {
		return Money{}, moneyNotMoney(s)
	}

	code := strings.ToLower(written)
	exp, known := CurrencyExponent(code)
	if !known {
		hint := "Use a three-letter code, like usd, eur or inr."
		if near := moneyNearest(code); near != "" {
			hint = `Did you mean "` + near + `"? ` + hint
		}
		return Money{}, &moneyProblem{
			Code:    "unknown_currency",
			Message: "`" + moneyClip(written) + "` is not an active ISO 4217 currency.",
			Hint:    hint,
		}
	}

	if len(fracPart) > exp {
		example := moneyRoundHalfUp(intPart, fracPart, exp) + " " + code
		hint := "Round it to " + strconv.Itoa(exp) + " decimals, like \"" + example + "\"."
		if exp == 0 {
			hint = "Write a whole number, like \"" + example + "\"."
		}
		return Money{}, &moneyProblem{
			Code:    "money_precision",
			Message: "`" + moneyClip(s) + "` has more decimals than " + code + " allows (" + strconv.Itoa(exp) + ").",
			Hint:    hint,
		}
	}

	// All digits of the amount in minor units, padded to the currency's decimals.
	minor := strings.TrimLeft(intPart+fracPart+strings.Repeat("0", exp-len(fracPart)), "0")
	if minor == "" {
		return Money{Amount: 0, Currency: code}, nil
	}
	if len(minor) > moneyMaxDigits {
		return Money{}, moneyTooLarge(s)
	}
	n, err := strconv.ParseUint(minor, 10, 64)
	if err != nil || n > MaxMinor {
		return Money{}, moneyTooLarge(s)
	}
	return Money{Amount: int64(n), Currency: code}, nil
}

// CurrencyExponent returns the number of minor-unit decimals for a lowercase ISO 4217 code.
func CurrencyExponent(code string) (int, bool) {
	exp, ok := currencyExponents[code]
	return exp, ok
}

// String renders m as "<decimal> <currency>" with exactly the currency's decimals, e.g. "50.00 usd", "500 jpy".
func (m Money) String() string {
	exp, _ := CurrencyExponent(m.Currency)
	sign := ""
	abs := uint64(m.Amount)
	if m.Amount < 0 {
		sign = "-"
		abs = -abs
	}
	digits := strconv.FormatUint(abs, 10)
	if exp > 0 {
		if len(digits) <= exp {
			digits = strings.Repeat("0", exp-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-exp] + "." + digits[len(digits)-exp:]
	}
	return sign + digits + " " + m.Currency
}

// splitMoney checks the shape "<digits>[.<digits>] <abc>" and returns its parts.
func splitMoney(s string) (intPart, fracPart, currency string, ok bool) {
	amount, currency, found := strings.Cut(s, " ")
	if !found || len(currency) != 3 || !moneyLetters(currency) {
		return "", "", "", false
	}
	intPart, fracPart, hasDot := strings.Cut(amount, ".")
	if intPart == "" || !moneyDigits(intPart) {
		return "", "", "", false
	}
	if hasDot && (fracPart == "" || !moneyDigits(fracPart)) {
		return "", "", "", false
	}
	return intPart, fracPart, currency, true
}

// moneyRoundHalfUp rounds intPart.fracPart half up to exp decimals and renders it.
func moneyRoundHalfUp(intPart, fracPart string, exp int) string {
	digits := []byte(strings.TrimLeft(intPart, "0") + fracPart[:exp])
	if fracPart[exp] >= '5' {
		i := len(digits) - 1
		for ; i >= 0 && digits[i] == '9'; i-- {
			digits[i] = '0'
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		} else {
			digits[i]++
		}
	}
	// Keep at least one digit before the decimal point.
	for len(digits) < exp+1 {
		digits = append([]byte{'0'}, digits...)
	}
	whole := string(digits[:len(digits)-exp])
	if exp == 0 {
		return whole
	}
	return whole + "." + string(digits[len(digits)-exp:])
}

// moneyCommon are checked first, so a typo of a common code suggests it (usx → usd, not ugx).
var moneyCommon = []string{"usd", "eur", "gbp", "inr", "jpy", "cad", "aud", "chf", "cny", "sgd"}

// moneyNearest returns a known code one edit away from code, or "": a common code first (transposed
// letters count as one edit), else the alphabetically first one.
func moneyNearest(code string) string {
	for _, c := range moneyCommon {
		if moneyEditOne(code, c) || damerau(code, c) == 1 {
			return c
		}
	}
	best := ""
	for known := range currencyExponents {
		if moneyEditOne(code, known) && (best == "" || known < best) {
			best = known
		}
	}
	return best
}

// moneyEditOne reports whether a and b differ by exactly one insertion, deletion or substitution.
func moneyEditOne(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	switch len(b) - len(a) {
	case 0:
		diff := 0
		for i := 0; i < len(a); i++ {
			if a[i] != b[i] {
				diff++
			}
		}
		return diff == 1
	case 1:
		for i := 0; i < len(a); i++ {
			if a[i] != b[i] {
				return a[i:] == b[i+1:]
			}
		}
		return true
	default:
		return false
	}
}

func moneyNotMoney(s string) *moneyProblem {
	return &moneyProblem{
		Code:    "invalid_money",
		Message: "`" + moneyClip(s) + "` is not an amount of money.",
		Hint:    `Write money as a string with a currency, like "50.00 usd".`,
	}
}

func moneyTooLarge(s string) *moneyProblem {
	return &moneyProblem{
		Code:    "invalid_money",
		Message: "`" + moneyClip(s) + "` is too large.",
		Hint:    "The largest amount is 9,007,199,254,740,991 in minor units.",
	}
}

// moneyClip shortens user text to moneyQuoteLimit runes, ending with "…" when cut.
func moneyClip(s string) string {
	if utf8.RuneCountInString(s) <= moneyQuoteLimit {
		return s
	}
	n := 0
	for i := range s {
		if n == moneyQuoteLimit {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

func moneyDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func moneyLetters(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i] | 0x20
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// FormatMoney writes minor units for people: "$4,000.00", "¥500", "1,250.00 chf" (S28's
// suggestion copy uses the same form as the budget messages).
func FormatMoney(amount int64, currency string) string { return formatMoney(amount, currency) }
