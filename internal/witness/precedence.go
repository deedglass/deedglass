package witness

import "sort"

// Precedence is contract §6 applied to one set of reasons: any violation gives MISMATCH; else any
// gap gives UNVERIFIABLE; else at least one pass gives VERIFIED; else (no checks) UNVERIFIABLE.
// Notes are ignored.
func Precedence(rs []Reason) Verdict {
	var pass, gap, violation bool
	for _, r := range rs {
		switch r.Class {
		case Violation:
			violation = true
		case Gap:
			gap = true
		case Pass:
			pass = true
		}
	}
	switch {
	case violation:
		return Mismatch
	case gap:
		return Unverifiable
	case pass:
		return Verified
	}
	return Unverifiable
}

// Decision is the Witness's claim about one action: one level, one verdict and the method of that
// level, with every reason from every level.
type Decision struct {
	Level    Level
	Verdict  Verdict
	Method   string
	Required Level
	PerLevel map[Level]Verdict // Precedence over each level's own reasons, for the levels that produced any
	Reasons  []Reason          // all reasons, sorted by level, then class (violation, gap, pass, note), then code
}

// Flags are the claim-level codes Decide added (level_below_required, no_checks), for events.
func (d Decision) Flags() []string {
	out := []string{}
	for _, r := range d.Reasons {
		if r.Code == CodeLevelBelowRequired || r.Code == CodeNoChecks {
			out = append(out, string(r.Code))
		}
	}
	return out
}

// Decide is the one function that produces a verdict (ADR-042, the claim rule):
//
//  1. Any violation at any level gives MISMATCH, at the highest level that has one. Contradicting
//     evidence is never outranked.
//  2. Otherwise VERIFIED, at the highest level L where Precedence(byLevel[L]) is VERIFIED (at least
//     one pass, no gap) and L ≥ required. Gaps at other levels stay in Reasons, unchanged.
//  3. Otherwise UNVERIFIABLE:
//     a. when some level was clean but below required, at the highest clean level, with
//     level_below_required;
//     b. else at the highest level that produced a pass, gap or violation, with no_checks when
//     no level did (then the level is L0).
//
// required outside L0..L2 counts as L0. A level's method comes from methods; a missing one is
// "reported" at L0 and "readback" at L1.
func Decide(required Level, byLevel map[Level][]Reason, methods map[Level]string) Decision {
	if !required.Valid() {
		required = L0
	}
	d := Decision{Required: required, PerLevel: map[Level]Verdict{}}
	var all []Reason
	highestViolation, highestClean, highestCleanBelow, highestAny := Level(""), Level(""), Level(""), Level("")
	for _, l := range Levels {
		rs := byLevel[l]
		all = append(all, rs...)
		counted := false
		for _, r := range rs {
			if r.Class != Note {
				counted = true
			}
		}
		if !counted {
			continue
		}
		highestAny = l
		v := Precedence(rs)
		d.PerLevel[l] = v
		switch {
		case v == Mismatch:
			highestViolation = l
		case v == Verified && l.Rank() >= required.Rank():
			highestClean = l
		case v == Verified:
			highestCleanBelow = l
		}
	}
	switch {
	case highestViolation != "":
		d.Level, d.Verdict = highestViolation, Mismatch
	case highestClean != "":
		d.Level, d.Verdict = highestClean, Verified
	case highestCleanBelow != "":
		d.Level, d.Verdict = highestCleanBelow, Unverifiable
		all = append(all, R(highestCleanBelow, CodeLevelBelowRequired, "claim",
			map[string]any{"required": string(required), "reached": string(highestCleanBelow)}))
	case highestAny != "":
		d.Level, d.Verdict = highestAny, Unverifiable
	default:
		d.Level, d.Verdict = L0, Unverifiable
		all = append(all, R(L0, CodeNoChecks, "claim", nil))
	}
	d.Method = methods[d.Level]
	if d.Method == "" {
		d.Method = defaultMethod(d.Level)
	}
	sortReasons(all)
	if all == nil {
		all = []Reason{}
	}
	d.Reasons = all
	return d
}

func defaultMethod(l Level) string {
	switch l {
	case L1:
		return "readback"
	case L2:
		return "postgres.pgoutput"
	}
	return "reported"
}

// sortReasons orders reasons by level, then class (violation, gap, pass, note), then code. It is
// stable, so reasons with the same key keep the order they were found in.
func sortReasons(rs []Reason) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.Level != b.Level {
			return a.Level.Rank() < b.Level.Rank()
		}
		if a.Class != b.Class {
			return a.Class.rank() < b.Class.rank()
		}
		return a.Code < b.Code
	})
}
