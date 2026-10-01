package witness

// Verdict is the Witness's answer about one action (contract §6). Only Decide, in precedence.go,
// returns Verified; verdict_guard_test.go fails the build if any other file produces it.
type Verdict string

// The three verdicts.
const (
	Verified     Verdict = "VERIFIED"
	Mismatch     Verdict = "MISMATCH"
	Unverifiable Verdict = "UNVERIFIABLE"
)

// Class is what a reason means for the verdict. Only the catalog assigns it.
type Class string

// The four classes.
const (
	Pass      Class = "pass"      // evidence that agrees with what the tool said happened
	Violation Class = "violation" // evidence that contradicts it
	Gap       Class = "gap"       // evidence Deedglass needed and couldn't get
	Note      Class = "note"      // context; never affects a verdict
)

// rank orders classes for sorting reasons: violation, gap, pass, note.
func (c Class) rank() int {
	switch c {
	case Violation:
		return 0
	case Gap:
		return 1
	case Pass:
		return 2
	}
	return 3
}
