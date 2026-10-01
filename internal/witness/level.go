package witness

// Level is how close to the system of record a piece of evidence came (contract §6):
// L0 Reported (the tool's own answer), L1 Confirmed (a read-back from the target),
// L2 Witnessed (the system of record's change stream, S21).
type Level string

// The three levels.
const (
	L0 Level = "L0"
	L1 Level = "L1"
	L2 Level = "L2"
)

// Levels lists the levels from the weakest to the strongest.
var Levels = []Level{L0, L1, L2}

// Rank orders levels: 0, 1, 2; anything else is -1.
func (l Level) Rank() int {
	switch l {
	case L0:
		return 0
	case L1:
		return 1
	case L2:
		return 2
	}
	return -1
}

// Valid reports whether l is one of the three levels.
func (l Level) Valid() bool { return l.Rank() >= 0 }

// ParseLevel reads a level, with L0 for "" (the policy default, S10). ok is false for anything else.
func ParseLevel(s string) (Level, bool) {
	if s == "" {
		return L0, true
	}
	l := Level(s)
	return l, l.Valid()
}

// Title is the level with its name, as the docs and the Console show it: "L1 Confirmed".
func (l Level) Title() string {
	switch l {
	case L0:
		return "L0 Reported"
	case L1:
		return "L1 Confirmed"
	case L2:
		return "L2 Witnessed"
	}
	return string(l)
}
