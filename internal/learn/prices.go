package learn

// Model prices for the optional Claude phrasing (S28 §3.7), per million tokens in US dollars.
// Source: Anthropic's model and pricing pages (https://platform.claude.com/docs/en/about-claude/models/overview,
// https://www.anthropic.com/pricing), checked 2026-10-04. A model missing here is priced at the
// most expensive row, so the monthly cap errs on the side of spending less.

// PricesCheckedOn is when the table was last checked against the source.
const PricesCheckedOn = "2026-10-04"

// ModelPrice is one model's price per million tokens, in micro-dollars.
type ModelPrice struct {
	InputPerMTokMicros  int64
	OutputPerMTokMicros int64
}

// Prices are the models the phrasing client may use.
var Prices = map[string]ModelPrice{
	"claude-sonnet-5":   {InputPerMTokMicros: 2_000_000, OutputPerMTokMicros: 10_000_000},
	"claude-sonnet-5-5": {InputPerMTokMicros: 2_000_000, OutputPerMTokMicros: 10_000_000},
	"claude-haiku-4-5":  {InputPerMTokMicros: 1_000_000, OutputPerMTokMicros: 5_000_000},
	"claude-opus-5":     {InputPerMTokMicros: 5_000_000, OutputPerMTokMicros: 25_000_000},
	"claude-opus-5-5":   {InputPerMTokMicros: 4_000_000, OutputPerMTokMicros: 20_000_000},
}

// DefaultPhrasingModel is used unless DEEDGLASS_ANTHROPIC_MODEL names another.
const DefaultPhrasingModel = "claude-sonnet-5-5"

// CostMicros is a call's cost in micro-dollars, rounded up.
func CostMicros(model string, in, out int) int64 {
	p, ok := Prices[model]
	if !ok {
		p = ModelPrice{InputPerMTokMicros: 5_000_000, OutputPerMTokMicros: 25_000_000}
	}
	n := int64(in)*p.InputPerMTokMicros + int64(out)*p.OutputPerMTokMicros
	return (n + 999_999) / 1_000_000
}
