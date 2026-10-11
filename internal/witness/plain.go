package witness

// Plain names (founder decision, Sunday master plan §1, DL-075, ADR-078): the words a person reads for a
// level and a verdict. The wire values (L0, L1, L2; VERIFIED, MISMATCH, UNVERIFIABLE) never change:
// they stay in the API, the deed, JSON output, flags and the database. One catalog, so the CLI,
// the scan report and the docs say the same words; the Console mirrors it in web/src/lib/copy.ts.
const (
	PlainL0 = "Claimed" // L0: the tool's own answer
	PlainL1 = "Checked" // L1: a read-back from the target
	PlainL2 = "Proven"  // L2: the system of record's change log

	PlainVerified     = "Matches"       // VERIFIED
	PlainMismatch     = "Doesn't match" // MISMATCH
	PlainUnverifiable = "Can't check"   // UNVERIFIABLE
)

// Plain is the level's plain name: Claimed, Checked or Proven. Anything else is returned as is.
func (l Level) Plain() string {
	switch l {
	case L0:
		return PlainL0
	case L1:
		return PlainL1
	case L2:
		return PlainL2
	}
	return string(l)
}

// PlainLevel is Plain for a level held as a string ("L1" → "Checked").
func PlainLevel(s string) string { return Level(s).Plain() }

// PlainVerdict is a verdict's plain name: Matches, Doesn't match or Can't check. Anything else is
// returned as is.
func PlainVerdict(v string) string {
	switch Verdict(v) {
	case Verified:
		return PlainVerified
	case Mismatch:
		return PlainMismatch
	case Unverifiable:
		return PlainUnverifiable
	}
	return v
}
