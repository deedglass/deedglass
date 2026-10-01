package witness

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// classSet is which classes of reason one level produced.
type classSet struct{ pass, gap, violation bool }

// allSets is ∅, P, G, V, PG, PV, GV, PGV.
var allSets = func() []classSet {
	var out []classSet
	for m := range 8 {
		out = append(out, classSet{pass: m&1 != 0, gap: m&2 != 0, violation: m&4 != 0})
	}
	return out
}()

func (c classSet) String() string {
	s := ""
	if c.pass {
		s += "P"
	}
	if c.gap {
		s += "G"
	}
	if c.violation {
		s += "V"
	}
	if s == "" {
		return "-"
	}
	return s
}

// reasonsFor builds real catalog reasons of each class at a level.
func reasonsFor(l Level, c classSet) []Reason {
	var out []Reason
	switch l {
	case L0:
		if c.pass {
			out = append(out, R(L0, CodeToolReportedSuccess, "status", nil))
		}
		if c.gap {
			out = append(out, R(L0, CodeOutcomeUnknown, "status", map[string]any{"cause": "timeout"}))
		}
		if c.violation {
			out = append(out, R(L0, CodePostconditionFailed, "readback.expect", nil))
		}
	case L1:
		if c.pass {
			out = append(out, R(L1, CodeReadbackMatched, "readback.expect", map[string]any{"readback": "orders.get", "fields": "status"}))
		}
		if c.gap {
			out = append(out, R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": "orders.get", "error_code": "upstream_error"}))
		}
		if c.violation {
			out = append(out, R(L1, CodePostconditionFailed, "readback.expect", nil))
		}
	case L2:
		if c.pass {
			out = append(out, R(L2, CodeRowWritten, "row.xmin", nil))
		}
		if c.gap {
			out = append(out, R(L2, CodeInternalError, "l2", nil))
		}
		if c.violation {
			out = append(out, R(L2, CodeValueMismatch, "row.value", nil))
		}
	}
	return out
}

// oracle is §3.3 restated literally, without Decide, Precedence or Level.Rank.
func oracle(required Level, sets [3]classSet) (Level, Verdict, []string) {
	names := [3]Level{"L0", "L1", "L2"}
	req := 0
	for i, n := range names {
		if n == required {
			req = i
		}
	}
	// 1. Any violation anywhere: MISMATCH at the highest level with one.
	for i := 2; i >= 0; i-- {
		if sets[i].violation {
			return names[i], "MISMATCH", []string{}
		}
	}
	// 2. VERIFIED at the highest level with a pass, no gap, no violation, at or above required.
	for i := 2; i >= 0; i-- {
		if sets[i].pass && !sets[i].gap && !sets[i].violation && i >= req {
			return names[i], "VERIFIED", []string{}
		}
	}
	// 3a. A clean level below required: UNVERIFIABLE there, with level_below_required.
	for i := 2; i >= 0; i-- {
		if sets[i].pass && !sets[i].gap && i < req {
			return names[i], "UNVERIFIABLE", []string{"level_below_required"}
		}
	}
	// 3b. The highest level with any pass, gap or violation; none: L0 with no_checks.
	for i := 2; i >= 0; i-- {
		if sets[i].pass || sets[i].gap || sets[i].violation {
			return names[i], "UNVERIFIABLE", []string{}
		}
	}
	return "L0", "UNVERIFIABLE", []string{"no_checks"}
}

// oracleLevel is §6 precedence over one level's classes.
func oracleLevel(c classSet) Verdict {
	switch {
	case c.violation:
		return "MISMATCH"
	case c.gap:
		return "UNVERIFIABLE"
	case c.pass:
		return "VERIFIED"
	}
	return "UNVERIFIABLE"
}

var testMethods = map[Level]string{L0: "reported", L1: "readback.rest.get", L2: "postgres.pgoutput"}

// forEachCase runs fn over 8³ level combinations × 3 required levels.
func forEachCase(fn func(required Level, sets [3]classSet)) int {
	n := 0
	for _, a := range allSets {
		for _, b := range allSets {
			for _, c := range allSets {
				for _, req := range Levels {
					fn(req, [3]classSet{a, b, c})
					n++
				}
			}
		}
	}
	return n
}

func byLevelOf(sets [3]classSet) map[Level][]Reason {
	m := map[Level][]Reason{}
	for i, l := range Levels {
		if rs := reasonsFor(l, sets[i]); len(rs) > 0 {
			m[l] = rs
		}
	}
	return m
}

func TestDecideExhaustive(t *testing.T) {
	n := forEachCase(func(req Level, sets [3]classSet) {
		name := fmt.Sprintf("L0=%s L1=%s L2=%s required=%s", sets[0], sets[1], sets[2], req)
		in := byLevelOf(sets)
		inCount := 0
		for _, rs := range in {
			inCount += len(rs)
		}
		d := Decide(req, in, testMethods)
		wl, wv, wf := oracle(req, sets)
		if d.Level != wl || d.Verdict != wv {
			t.Errorf("%s: got %s · %s, want %s · %s", name, d.Level, d.Verdict, wl, wv)
		}
		if got := d.Flags(); !slices.Equal(got, wf) {
			t.Errorf("%s: flags %v, want %v", name, got, wf)
		}
		if d.Required != req {
			t.Errorf("%s: required %s", name, d.Required)
		}
		if d.Method != testMethods[d.Level] {
			t.Errorf("%s: method %q, want %q", name, d.Method, testMethods[d.Level])
		}
		if len(d.Reasons) != inCount+len(wf) {
			t.Errorf("%s: %d reasons, want %d", name, len(d.Reasons), inCount+len(wf))
		}
		for i, l := range Levels {
			v, ok := d.PerLevel[l]
			ran := sets[i].pass || sets[i].gap || sets[i].violation
			if ok != ran || (ran && v != oracleLevel(sets[i])) {
				t.Errorf("%s: PerLevel[%s] = %q (%v), want %q (%v)", name, l, v, ok, oracleLevel(sets[i]), ran)
			}
		}
		for i := 1; i < len(d.Reasons); i++ {
			a, b := d.Reasons[i-1], d.Reasons[i]
			if a.Level.Rank() > b.Level.Rank() || (a.Level == b.Level && a.Class.rank() > b.Class.rank()) {
				t.Errorf("%s: reasons out of order at %d: %s/%s before %s/%s", name, i, a.Level, a.Class, b.Level, b.Class)
			}
		}
		// Rule 3: a gap never becomes MISMATCH; rule 2: VERIFIED only at a clean level ≥ required.
		anyV := sets[0].violation || sets[1].violation || sets[2].violation
		if (d.Verdict == Mismatch) != anyV {
			t.Errorf("%s: MISMATCH = %v, any violation = %v", name, d.Verdict == Mismatch, anyV)
		}
	})
	if n != 1536 {
		t.Fatalf("%d cases, want 1536", n)
	}
	t.Logf("%d cases", n)
}

func TestDecideNotesIgnored(t *testing.T) {
	notes := func(l Level) []Reason {
		return []Reason{
			R(l, CodeReadbackSkipped, "readback", map[string]any{"why": "test"}),
			R(l, CodeSchemaSkipped, "response.schema", nil),
		}
	}
	n := forEachCase(func(req Level, sets [3]classSet) {
		plain := Decide(req, byLevelOf(sets), nil)
		with := byLevelOf(sets)
		for _, l := range Levels {
			with[l] = append(notes(l), with[l]...)
		}
		d := Decide(req, with, nil)
		if d.Level != plain.Level || d.Verdict != plain.Verdict || !slices.Equal(d.Flags(), plain.Flags()) {
			t.Errorf("L0=%s L1=%s L2=%s required=%s: with notes %s · %s %v, without %s · %s %v",
				sets[0], sets[1], sets[2], req, d.Level, d.Verdict, d.Flags(), plain.Level, plain.Verdict, plain.Flags())
		}
		if len(d.PerLevel) != len(plain.PerLevel) {
			t.Errorf("notes changed PerLevel: %v vs %v", d.PerLevel, plain.PerLevel)
		}
	})
	if n != 1536 {
		t.Fatalf("%d cases", n)
	}
	// Notes alone are no checks.
	d := Decide(L0, map[Level][]Reason{L0: notes(L0), L1: notes(L1), L2: notes(L2)}, nil)
	if d.Level != L0 || d.Verdict != Unverifiable || !slices.Equal(d.Flags(), []string{"no_checks"}) {
		t.Errorf("notes only: %s · %s %v", d.Level, d.Verdict, d.Flags())
	}
}

func TestDecideWorkedTable(t *testing.T) {
	p0 := R(L0, CodeToolReportedSuccess, "status", nil)
	p1 := R(L1, CodeReadbackMatched, "readback.expect", map[string]any{"readback": "orders.get", "fields": "status"})
	v1 := R(L1, CodePostconditionFailed, "readback.expect", nil)
	g1 := R(L1, CodeReadbackFailed, "readback", map[string]any{"readback": "orders.get", "error_code": "upstream_error"})
	g0 := R(L0, CodeResponseSchemaInvalid, "response.schema", map[string]any{"path": "body.status"})
	g2 := R(L2, CodeInternalError, "l2", nil) // l2_timeout once S21 registers it
	v2 := R(L2, CodeValueMismatch, "row.value", nil)
	cases := []struct {
		name       string
		l0, l1, l2 []Reason
		required   Level
		level      Level
		verdict    Verdict
		flags      []string
		listed     Code // a reason that must stay listed
	}{
		{"pass", []Reason{p0}, nil, nil, L0, L0, Verified, []string{}, ""},
		{"pass, L1 violation", []Reason{p0}, []Reason{v1}, nil, L0, L1, Mismatch, []string{}, ""},
		{"pass, L1 gap, required L0", []Reason{p0}, []Reason{g1}, nil, L0, L0, Verified, []string{}, CodeReadbackFailed},
		{"pass, L1 gap, required L1", []Reason{p0}, []Reason{g1}, nil, L1, L0, Unverifiable, []string{"level_below_required"}, CodeReadbackFailed},
		{"L0 gap, L1 pass", []Reason{g0}, []Reason{p1}, nil, L0, L1, Verified, []string{}, CodeResponseSchemaInvalid},
		{"pass, pass, L2 gap, required L2", []Reason{p0}, []Reason{p1}, []Reason{g2}, L2, L1, Unverifiable, []string{"level_below_required"}, CodeInternalError},
		{"pass, pass, L2 violation", []Reason{p0}, []Reason{p1}, []Reason{v2}, L0, L2, Mismatch, []string{}, ""},
		{"nothing", nil, nil, nil, L0, L0, Unverifiable, []string{"no_checks"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := map[Level][]Reason{L0: c.l0, L1: c.l1, L2: c.l2}
			d := Decide(c.required, in, nil)
			if d.Level != c.level || d.Verdict != c.verdict {
				t.Fatalf("got %s · %s, want %s · %s", d.Level, d.Verdict, c.level, c.verdict)
			}
			if !slices.Equal(d.Flags(), c.flags) {
				t.Errorf("flags %v, want %v", d.Flags(), c.flags)
			}
			if c.listed != "" && !slices.ContainsFunc(d.Reasons, func(r Reason) bool { return r.Code == c.listed }) {
				t.Errorf("%s isn't listed in %v", c.listed, d.Reasons)
			}
			if d.Method != defaultMethod(d.Level) {
				t.Errorf("method %q", d.Method)
			}
		})
	}
}

func TestDecideRequiredOutOfRange(t *testing.T) {
	d := Decide("L9", map[Level][]Reason{L0: {R(L0, CodeToolReportedSuccess, "status", nil)}}, nil)
	if d.Required != L0 || d.Verdict != Verified || d.Level != L0 {
		t.Errorf("got %+v", d)
	}
	if d.Reasons == nil {
		t.Error("Reasons is nil")
	}
	if e := Decide(L0, nil, nil); e.Reasons == nil || len(e.Reasons) != 1 {
		t.Errorf("empty: %+v", e.Reasons)
	}
}

func TestPrecedence(t *testing.T) {
	p := R(L0, CodeToolReportedSuccess, "status", nil)
	g := R(L0, CodeOutcomeUnknown, "status", nil)
	v := R(L1, CodePostconditionFailed, "readback.expect", nil)
	n := R(L1, CodeReadbackSkipped, "readback", nil)
	cases := []struct {
		name string
		rs   []Reason
		want Verdict
	}{
		{"none", nil, Unverifiable},
		{"note", []Reason{n}, Unverifiable},
		{"pass", []Reason{p}, Verified},
		{"pass and note", []Reason{p, n}, Verified},
		{"gap", []Reason{g}, Unverifiable},
		{"pass and gap", []Reason{p, g}, Unverifiable},
		{"violation", []Reason{v}, Mismatch},
		{"violation and gap", []Reason{g, v}, Mismatch},
		{"all", []Reason{n, p, g, v}, Mismatch},
	}
	for _, c := range cases {
		if got := Precedence(c.rs); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// TestPrecedenceProperty checks the invariants over 100,000 seeded random reason lists.
func TestPrecedenceProperty(t *testing.T) {
	codes := Catalog()
	rng := rand.New(rand.NewPCG(19, 2026))
	const n = 100_000
	for i := range n {
		required := Levels[rng.IntN(3)]
		byLevel := map[Level][]Reason{}
		var flat []Reason
		for range rng.IntN(9) {
			l := Levels[rng.IntN(3)]
			e := codes[rng.IntN(len(codes))]
			r := R(l, e.Code, "property", e.Sample)
			byLevel[l] = append(byLevel[l], r)
			flat = append(flat, r)
		}
		// Gaps only, now and then.
		gapsOnly := i%10 == 0
		if gapsOnly {
			byLevel, flat = map[Level][]Reason{}, nil
			for range 1 + rng.IntN(4) {
				r := R(Levels[rng.IntN(3)], CodeReadbackFailed, "property", nil)
				byLevel[r.Level] = append(byLevel[r.Level], r)
				flat = append(flat, r)
			}
		}
		anyViolation := slices.ContainsFunc(flat, func(r Reason) bool { return r.Class == Violation })
		d := Decide(required, byLevel, nil)
		if (d.Verdict == Mismatch) != anyViolation {
			t.Fatalf("case %d: %s with any violation = %v: %v", i, d.Verdict, anyViolation, flat)
		}
		if (Precedence(flat) == Mismatch) != anyViolation {
			t.Fatalf("case %d: Precedence %s with any violation = %v", i, Precedence(flat), anyViolation)
		}
		if gapsOnly && d.Verdict != Unverifiable {
			t.Fatalf("case %d: gaps only gave %s", i, d.Verdict)
		}
		if d.Verdict == Verified {
			var pass, gap int
			for _, r := range byLevel[d.Level] {
				switch r.Class {
				case Pass:
					pass++
				case Gap, Violation:
					gap++
				}
			}
			if d.Level.Rank() < required.Rank() || pass == 0 || gap != 0 {
				t.Fatalf("case %d: VERIFIED at %s (required %s) with %d passes and %d gaps", i, d.Level, required, pass, gap)
			}
		}
	}
}
