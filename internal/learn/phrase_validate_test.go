package learn

import (
	"os"
	"strings"
	"testing"
)

// S28 §6 "Validator": a reply is used only when every check passes.

// valInput has template text without numbers, so every number a reply may use comes from Numbers
// in one of its normalized forms.
func valInput() *PhrasingInput {
	return &PhrasingInput{Kind: KindLoosen, Generator: GenLoosenApprovalRate, Tool: "postgres.execute", WindowDays: 14,
		Refs: map[string]string{"{table_1}": "table", "{agent_1}": "agent", "{rule_1}": "rule"},
		Numbers: []Number{
			{Key: "approved", Value: 401, Unit: "count"},
			{Key: "share", Value: 0.973, Unit: "rate"},
			{Key: "median_wait", Value: 1260, Unit: "seconds"},
			{Key: "limit", Value: 4800, Unit: "minor:usd"},
			{Key: "actions", Value: 1204, Unit: "count"},
			{Key: "window_days", Value: 14, Unit: "days"},
			{Key: "ratio", Value: 1.5, Unit: "ratio"},
		},
		TemplateTitle:     "Allow updates to {table_1} without approval",
		TemplateRationale: "People approved these updates to {table_1} by {agent_1} and denied none.",
	}
}

const validTitle = "Let updates to {table_1} run without approval"

// bannedPhrases reads scripts/banned_words.txt at run time, so this file names none of them.
func bannedPhrases(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/banned_words.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, strings.TrimPrefix(l, "@until-public "))
	}
	if len(out) < 20 {
		t.Fatalf("only %d banned phrases", len(out))
	}
	return out
}

func TestValidatePhrasing(t *testing.T) {
	t.Parallel()
	in := valInput()
	okRationale := "People approved 401 updates to {table_1} by {agent_1}, 97.3% of 1,204, after a median of 21 minutes. The limit stays at $48.00."
	if ok, why := ValidatePhrasing(in, validTitle, okRationale); !ok {
		t.Fatalf("valid reply refused: %s", why)
	}
	emoji := string(rune(0x1F680))
	cases := []struct {
		name, title, rationale, want string
	}{
		{"number not in input", validTitle, "People approved 402 updates to {table_1} by {agent_1}.", ValNumberNotInInput},
		{"rounded rate not given", validTitle, "People approved 97.4% of updates to {table_1} by {agent_1}.", ValNumberNotInInput},
		{"money not given", validTitle, "Refunds up to $49.00 to {table_1} by {agent_1}.", ValNumberNotInInput},
		{"placeholder that doesn't exist", validTitle, "People approved 401 updates to {table_2} by {agent_1} on {table_1}.", ValPlaceholderAdded},
		{"placeholder dropped", validTitle, "People approved 401 updates to {table_1}.", ValPlaceholderLost},
		{"exclamation", validTitle, "People approved 401 updates to {table_1} by {agent_1}!", ValExclamation},
		{"emoji", validTitle + " " + emoji, "People approved 401 updates to {table_1} by {agent_1}.", ValEmoji},
		{"emoji presentation selector", validTitle, "People approved 401 updates to {table_1} by {agent_1}." + string(rune(0xFE0F)), ValEmoji},
		{"dingbat", validTitle, "Done " + string(rune(0x2705)) + " for {table_1} by {agent_1}.", ValEmoji},
		{"I think", validTitle, "I think people approved 401 updates to {table_1} by {agent_1}.", ValFirstPerson},
		{"we", validTitle, "We saw 401 updates to {table_1} by {agent_1}.", ValFirstPerson},
		{"our", validTitle, "Our records show 401 updates to {table_1} by {agent_1}.", ValFirstPerson},
		{"title of 91 characters", "Let updates to {table_1} run " + strings.Repeat("x", 91-len("Let updates to {table_1} run ")), "People approved 401 updates to {table_1} by {agent_1}.", ValTitleLength},
		{"title with a line break", "Let updates\nrun {table_1}", "People approved 401 updates to {table_1} by {agent_1}.", ValTitleLength},
		{"rationale of 401 characters", validTitle, "{table_1} {agent_1} " + strings.Repeat("y", 401-len("{table_1} {agent_1} ")), ValRationaleLength},
		{"three sentences", validTitle, "People approved 401 updates. They went to {table_1}. By {agent_1}.", ValSentences},
		{"a question counts", validTitle, "Why wait? People approved 401 updates to {table_1}? By {agent_1}.", ValSentences},
		{"em dash", validTitle, "People approved 401 updates to {table_1} " + string(rune(0x2014)) + " by {agent_1}.", ValDash},
		{"en dash", validTitle, "People approved 401 updates to {table_1} " + string(rune(0x2013)) + " by {agent_1}.", ValDash},
		{"control character", validTitle, "People approved 401 updates to {table_1}\x07 by {agent_1}.", ValControlCharacters},
		{"empty title", "  ", "People approved 401 updates to {table_1} by {agent_1}.", ValEmpty},
		{"empty rationale", validTitle, "", ValEmpty},
	}
	for _, c := range cases {
		ok, why := ValidatePhrasing(in, c.title, c.rationale)
		if ok || why != c.want {
			t.Errorf("%s: ok=%v %q, want %q", c.name, ok, why, c.want)
		}
	}
	// Every banned phrase, built from the canonical list at run time.
	for _, w := range bannedPhrases(t) {
		ok, why := ValidatePhrasing(in, validTitle, "This change to {table_1} by {agent_1} is "+w+".")
		if ok || why != ValBannedWord {
			t.Errorf("banned %q: ok=%v %q", w, ok, why)
		}
	}
}

func TestValidatePhrasingNormalizedNumbers(t *testing.T) {
	t.Parallel()
	in := valInput()
	accepted := []string{
		"401", "1,204", "1204", "97.3%", "97%", "21 minutes", "1,260 seconds", "$48.00", "48.00 dollars", "4,800 cents", "14 days", "1.5 times",
	}
	for _, n := range accepted {
		r := "People approved " + n + " updates to {table_1} by {agent_1}."
		if ok, why := ValidatePhrasing(in, validTitle, r); !ok {
			t.Errorf("%q refused: %s", n, why)
		}
	}
	rejected := []string{"402", "1,205", "97.2%", "22 minutes", "$48.01", "15 days", "2 hours"}
	for _, n := range rejected {
		r := "People approved " + n + " updates to {table_1} by {agent_1}."
		if ok, why := ValidatePhrasing(in, validTitle, r); ok || why != ValNumberNotInInput {
			t.Errorf("%q: ok=%v %q", n, ok, why)
		}
	}
	// Numbers already in the template text (thresholds) are allowed too; placeholders' digits never count.
	in2 := valInput()
	in2.TemplateRationale = "Over 90% on 3 of the last 7 days for {table_1} by {agent_1}."
	if ok, why := ValidatePhrasing(in2, validTitle, "Above 90% on 3 of 7 days for {table_1} by {agent_1}."); !ok {
		t.Errorf("template numbers: %s", why)
	}
	// Digits inside a word or an id aren't numbers.
	if ok, why := ValidatePhrasing(in, validTitle, "Witnessed at L2 for {table_1} by {agent_1} with sha256 ids."); !ok {
		t.Errorf("L2: %s", why)
	}
	// A percentage rounded to a whole number is accepted for a rate; a 0 from seconds/3600 too.
	if ok, _ := ValidatePhrasing(in, validTitle, "People approved 0 hours of updates to {table_1} by {agent_1}."); !ok {
		t.Error("0 hours from 1,260 s")
	}
}

func TestValidatorHelpers(t *testing.T) {
	t.Parallel()
	if sentences("No end") != 1 || sentences("One. Two.") != 2 || sentences("v1.2 is out. Fine?") != 2 || sentences("a.b.c") != 1 {
		t.Error("sentences")
	}
	for _, r := range []rune{0x1F600, 0x1F680, 0x2600, 0x27BF, 0x1F9FF, 0xFE0F} {
		if !isEmoji(r) {
			t.Errorf("%U not an emoji", r)
		}
	}
	for _, r := range []rune{'a', '9', 0x2014, 0x203A, 0x00B7} {
		if isEmoji(r) {
			t.Errorf("%U is an emoji", r)
		}
	}
	if normNumber("1,204,000") != "1204000" {
		t.Error("normNumber")
	}
	a := allowedNumbers(valInput())
	for _, k := range []string{"401", "97.3%", "97%", "1260 second", "21 minute", "0 hour", "$48.00", "48.00 dollar", "4800 cent", "1204", "14", "14 day", "1.5"} {
		if !a[k] {
			t.Errorf("%q not allowed (%v)", k, a)
		}
	}
}

// A number is checked with its unit: 1,260 seconds is "21 minutes", never "21 hours"; 4,800 minor
// units of usd is "$48.00", never "4,800 dollars" (S28 §3.7: normalized forms).
func TestValidatePhrasingUnits(t *testing.T) {
	t.Parallel()
	in := valInput()
	for _, n := range []string{"21 hours", "21 days", "4,800 dollars", "$4,800"} {
		r := "People approved 401 updates to {table_1} by {agent_1}, " + n + "."
		if ok, _ := ValidatePhrasing(in, validTitle, r); ok {
			t.Errorf("BUG: %q passes the number check (only the digits are compared, not the unit)", n)
		}
	}
}
