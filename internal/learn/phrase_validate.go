package learn

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/policy"
)

// The phrasing validator (S28 §3.7). A reply is used only when every check passes; any failure
// keeps the template, with phrasing.reason "validation:<check>". The checks: lengths and shape,
// placeholders (the reply keeps every one the template used and adds none), numbers (every number
// in the reply appears in the input, in a normalized form), banned words, no "!", no emoji, no
// first person.

// Phrasing limits.
const (
	MaxPhrasedTitle     = 90
	MaxPhrasedRationale = 400
)

// Validation failure reasons.
const (
	ValEmpty             = "validation:empty"
	ValTitleLength       = "validation:title_length"
	ValRationaleLength   = "validation:rationale_length"
	ValSentences         = "validation:sentences"
	ValPlaceholderAdded  = "validation:placeholder_unknown"
	ValPlaceholderLost   = "validation:placeholder_dropped"
	ValNumberNotInInput  = "validation:number_not_in_input"
	ValBannedWord        = "validation:banned_word"
	ValExclamation       = "validation:exclamation"
	ValEmoji             = "validation:emoji"
	ValFirstPerson       = "validation:first_person"
	ValControlCharacters = "validation:control_characters"
	ValDash              = "validation:dash"
)

var numberRE = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.{])(\$?\d[\d,]*(?:\.\d+)?%?)(?:\s+(seconds?|minutes?|hours?|days?|weeks?|months?|cents?|dollars?|usd)\b)?`)

var firstPersonRE = regexp.MustCompile(`(?i)(?:^|[^A-Za-z'])(i|i'm|i've|i'd|i'll|me|my|mine|we|we're|we've|our|ours|us)(?:$|[^A-Za-z'])`)

// ValidatePhrasing checks a reply against its input. ok is false with the reason on any failure.
func ValidatePhrasing(in *PhrasingInput, title, rationale string) (bool, string) {
	title, rationale = strings.TrimSpace(title), strings.TrimSpace(rationale)
	if title == "" || rationale == "" {
		return false, ValEmpty
	}
	if utf8.RuneCountInString(title) > MaxPhrasedTitle || strings.ContainsAny(title, "\n\r") {
		return false, ValTitleLength
	}
	if utf8.RuneCountInString(rationale) > MaxPhrasedRationale {
		return false, ValRationaleLength
	}
	if sentences(rationale) > 2 {
		return false, ValSentences
	}
	all := title + "\n" + rationale
	for _, r := range all {
		switch {
		case unicode.IsControl(r) && r != '\n':
			return false, ValControlCharacters
		case isEmoji(r):
			return false, ValEmoji
		case r == '—' || r == '–':
			return false, ValDash
		}
	}
	if strings.Contains(all, "!") {
		return false, ValExclamation
	}
	// Placeholders: none invented, none dropped.
	used := map[string]bool{}
	for _, p := range placeholderRE.FindAllString(all, -1) {
		if _, ok := in.Refs[p]; !ok {
			return false, ValPlaceholderAdded
		}
		used[p] = true
	}
	for _, p := range placeholderRE.FindAllString(in.TemplateTitle+" "+in.TemplateRationale, -1) {
		if !used[p] {
			return false, ValPlaceholderLost
		}
	}
	stripped := placeholderRE.ReplaceAllString(all, " ")
	if w := policy.BannedWordIn(stripped); w != "" {
		return false, ValBannedWord
	}
	if firstPersonRE.MatchString(stripped) {
		return false, ValFirstPerson
	}
	allowed := allowedNumbers(in)
	for _, m := range numberRE.FindAllStringSubmatch(stripped, -1) {
		if !allowed[numberKey(m[1], m[2])] {
			return false, ValNumberNotInInput
		}
	}
	return true, ""
}

func sentences(s string) int {
	n := 0
	for i, r := range s {
		if r == '.' || r == '?' {
			// a period inside a number or an id isn't a sentence end
			if i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\n' {
				continue
			}
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

func isEmoji(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || (r >= 0x1F900 && r <= 0x1F9FF) || r == 0xFE0F
}

// normNumber drops thousands separators so 1,204 and 1204 compare equal.
func normNumber(s string) string { return strings.ReplaceAll(strings.TrimRight(s, ".,"), ",", "") }

// numberKey is a number with its unit word, normalized: "1204", "21 minute", "$48.00", "97.3%".
// A number is allowed only with the unit its input value had (21 minutes from 1,260 seconds,
// never 21 hours), so a reworded sentence can't change what a number means.
func numberKey(num, unit string) string {
	k := normNumber(num)
	u := strings.TrimSuffix(strings.ToLower(unit), "s")
	if u == "" {
		return k
	}
	return k + " " + u
}

// allowedNumbers are every normalized form of the input's numbers, plus the numbers already in
// the template text (thresholds such as 90% and 30 days), each with the unit it may carry.
func allowedNumbers(in *PhrasingInput) map[string]bool {
	ok := map[string]bool{}
	add := func(num, unit string) { ok[numberKey(num, unit)] = true }
	itoa := func(x float64) string { return strconv.FormatInt(int64(math.Round(x)), 10) }
	for _, n := range in.Numbers {
		v := n.Value
		switch {
		case n.Unit == "count":
			add(itoa(v), "")
		case n.Unit == "days":
			add(itoa(v), "")
			add(itoa(v), "days")
		case n.Unit == "rate":
			add(fmtPercent(v), "")
			add(itoa(v*100)+"%", "")
		case n.Unit == "seconds":
			add(itoa(v), "seconds")
			add(itoa(v/60), "minutes")
			add(itoa(v/3600), "hours")
			add(itoa(v/86400), "days")
		case strings.HasPrefix(n.Unit, "minor:"):
			cur := strings.TrimPrefix(n.Unit, "minor:")
			m := policy.FormatMoney(int64(v), cur)
			add(m, "")
			plain := strings.TrimSpace(strings.TrimSuffix(strings.TrimLeft(m, "$€£¥₹"), cur))
			add(plain, cur)
			if cur == "usd" {
				add(plain, "dollars")
			}
			add(itoa(v), "cents")
		default:
			add(strconv.FormatFloat(v, 'f', -1, 64), "")
		}
	}
	for _, m := range numberRE.FindAllStringSubmatch(placeholderRE.ReplaceAllString(in.TemplateTitle+" "+in.TemplateRationale, " "), -1) {
		add(m[1], m[2])
	}
	return ok
}
