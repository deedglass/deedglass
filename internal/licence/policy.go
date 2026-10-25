package licence

// Policy answers one question before any licence is opened: does this feature need a key on this
// build? DL-131 (free while we grow; DL-132 sets the review point): every feature is free,
// so Current turns every entitlement on and nothing reads a licence file or calls Dodo
// Payments or deedglass.com. The verifier, the token format and the licence commands stay as they
// are, so asking for a key again later is setting Current to KeyRequired (or listing the features
// that stay free in Free), not a rewrite.
type Policy struct {
	// AllFree turns every feature on without a key.
	AllFree bool
	// Free lists the features that run without a key when AllFree is false.
	Free []string
}

var (
	// FreeWhileWeGrow is DL-131's policy: every entitlement on, no key, no licence check.
	FreeWhileWeGrow = Policy{AllFree: true}
	// KeyRequired is the policy before DL-131: every Pro feature needs a licence that grants it.
	KeyRequired = Policy{}
)

// Current is this build's policy.
var Current = FreeWhileWeGrow

// IsFree reports whether feature runs without a key under p.
func (p Policy) IsFree(feature string) bool {
	if p.AllFree {
		return true
	}
	for _, f := range p.Free {
		if f == feature {
			return true
		}
	}
	return false
}
