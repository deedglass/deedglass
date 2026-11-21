// Licensed under ee/LICENSE (FSL-1.1-ALv2).
package scribeeval

import (
	"errors"
	"fmt"
)

var (
	ErrEmptyBundle = errors.New("scribe-eval: empty transaction bundle")
)

// TransactionBundle models an atomic clinical bundle containing multiple related resource events.
type TransactionBundle struct {
	BundleID string       `json:"bundle_id"`
	Events   []*EvalEvent `json:"events"`
}

// BundleVerdict summarizes the all-or-nothing evaluation of a composite transaction bundle.
type BundleVerdict struct {
	BundleID       string         `json:"bundle_id"`
	ResourceCount  int            `json:"resource_count"`
	UnifiedVerdict WitnessVerdict `json:"unified_verdict"`
	AtomicCommit   bool           `json:"atomic_commit"`
	FailureReason  string         `json:"failure_reason,omitempty"`
}

// ValidateBundleAtomicity enforces Hazard CH-20: if any resource in a multi-resource bundle
// fails sanitization, diverges, or returns MISMATCH/UNVERIFIABLE, the entire bundle fails closed.
func ValidateBundleAtomicity(bundle *TransactionBundle, sanitizer Sanitizer) (BundleVerdict, error) {
	if bundle == nil || len(bundle.Events) == 0 {
		return BundleVerdict{UnifiedVerdict: VerdictUnverifiable}, ErrEmptyBundle
	}

	verdict := BundleVerdict{
		BundleID:      bundle.BundleID,
		ResourceCount: len(bundle.Events),
	}

	for i, e := range bundle.Events {
		if e == nil {
			verdict.UnifiedVerdict = VerdictUnverifiable
			verdict.FailureReason = fmt.Sprintf("event index %d is nil", i)
			return verdict, ErrNilEvent
		}

		if err := sanitizer.ValidateAndSanitize(e); err != nil {
			verdict.UnifiedVerdict = VerdictUnverifiable
			verdict.FailureReason = fmt.Sprintf("event index %d failed sanitization: %v", i, err)
			return verdict, fmt.Errorf("%w: resource %s failed validation: %v", ErrIncompleteTransactionBundle, e.FHIRResourceType, err)
		}

		// Fail closed on any non-verified outcome
		if e.WitnessVerdict == VerdictMismatch || e.WitnessVerdict == VerdictUnverifiable {
			verdict.UnifiedVerdict = VerdictUnverifiable
			verdict.FailureReason = fmt.Sprintf("event index %d had unverified verdict %s", i, e.WitnessVerdict)
			return verdict, fmt.Errorf("%w: resource %s diverged (%s)", ErrIncompleteTransactionBundle, e.FHIRResourceType, e.WitnessVerdict)
		}
	}

	verdict.UnifiedVerdict = VerdictL2ProvenMatch
	verdict.AtomicCommit = true
	return verdict, nil
}
