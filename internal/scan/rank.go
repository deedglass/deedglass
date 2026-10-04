package scan

import (
	"math"
	"regexp"
	"sort"
)

// reMoney names tables that hold money or people (the M term of the rank).
var reMoney = regexp.MustCompile(`(?i)(order|payment|refund|invoice|charge|ledger|balance|account|customer|user|subscription|payout|transaction)`)

// TableScore is S32 §3.13.3's rank, exactly:
//
//	(40·D + 25·U + 10·I + 15·T + 10·C + 10·M) × (0.5 + 0.5·min(1, log10(1 + rows) / 6))
//
// with a fixed factor of 0.75 for a table that was never analysed.
func TableScore(t Table) float64 {
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	has := func(c string) bool {
		for _, x := range t.Can {
			if x == c {
				return true
			}
		}
		return false
	}
	trig := false
	for _, tr := range t.Triggers {
		trig = trig || tr.Enabled
	}
	base := 40*b(has("DELETE") || has("TRUNCATE")) + 25*b(has("UPDATE")) + 10*b(has("INSERT")) + 15*b(trig) +
		10*b(len(t.CascadesTo) > 0) + 10*b(reMoney.MatchString(t.Table))
	factor := 0.75
	if t.EstRows >= 0 {
		factor = 0.5 + 0.5*math.Min(1, math.Log10(1+float64(t.EstRows))/6)
	}
	return base * factor
}

// rank scores every table and sorts by score, ties by schema.table ascending.
func rank(ts []Table) {
	for i := range ts {
		ts[i].Score = math.Round(TableScore(ts[i])*10) / 10
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Score != ts[j].Score {
			return ts[i].Score > ts[j].Score
		}
		return ts[i].Name() < ts[j].Name()
	})
}
