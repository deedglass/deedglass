package deed

import (
	"fmt"
	"strings"
)

// Message returns the one sentence and the one next step a person sees for e. S05's CLI and the
// browser verifier show exactly these (S04 §5); a test keeps every Code covered and the copy calm
// (no exclamation marks, sentence case, a full stop at the end).
func Message(e *VerifyError) (sentence, next string) {
	d := deedRef(e.Seq)
	switch e.Code {
	case CodeMalformed:
		detail := e.Detail
		if detail == "" {
			detail = "unreadable"
		}
		return fmt.Sprintf("%s is not valid strict JSON (%s).", d, detail),
			"Export it again from the issuer; do not edit deeds by hand."
	case CodeUnsupportedVersion:
		return fmt.Sprintf("%s uses deed format v%d. This verifier reads v1.", d, e.Version), "Update deedglass."
	case CodeSchemaInvalid:
		return fmt.Sprintf("%s has an invalid field: %s.", d, orDash(e.Field)), "Export it again from the issuer."
	case CodeIDMismatch:
		return fmt.Sprintf("%s's id does not match its body.", d), "Export it again from the issuer."
	case CodeBodyHashMismatch:
		return fmt.Sprintf("%s was changed after it was sealed.", d),
			fmt.Sprintf("Get a fresh export from the issuer and compare %s.", strings.ToLower(d[:1])+d[1:])
	case CodeSealKeyMismatch:
		return fmt.Sprintf("%s names two different seal keys.", d), "Treat this deed as altered."
	case CodeSealKeyUnknown:
		return fmt.Sprintf("%s names seal key %s, which is not in the key set.", d, orDash(e.Key)),
			"Fetch the issuer's keys: --keys <issuer>/.well-known/deedglass/keys.json"
	case CodeSealKeyRejected:
		return fmt.Sprintf("The key set contains an unsafe key (%s).", orDash(e.Key)),
			"Do not use this key set. Fetch it again from the issuer."
	case CodeSealKeyNotValidAt:
		return fmt.Sprintf("%s was sealed at %s, outside seal key %s's active window.", d, orDash(e.IssuedAt), orDash(e.Key)),
			"Treat this deed as altered, or check you have the issuer's full key set."
	case CodeSealKeyRevoked:
		return fmt.Sprintf("%s was sealed by revoked key %s.", d, orDash(e.Key)),
			"Ask the issuer which deeds they re-sealed after the revocation."
	case CodeSealInvalid:
		return fmt.Sprintf("%s's seal does not match its contents.", d), "Treat this deed as altered."
	case CodeNonCanonical:
		return fmt.Sprintf("%s is not in canonical form (--strict).", d),
			"Re-export it; canonical bytes come straight from the Ledger."
	case CodeChainGenesisMismatch:
		return "Deed 1 does not start this org's chain.", "Check the org id; the export may belong to another org."
	case CodeChainSeqGap:
		return fmt.Sprintf("Deed %d is missing. The chain jumps from %d to %d.", e.Want, e.Prev, e.Seq),
			fmt.Sprintf("A deed was removed or the export is incomplete. Ask for seq %d–%d.", e.Prev, e.Seq)
	case CodeChainSeqOutOfOrder:
		return fmt.Sprintf("%s appears out of order (after %d).", d, e.Prev), "Sort by seq, or export again."
	case CodeChainPrevHashMismatch:
		return fmt.Sprintf("%s does not link to deed %d.", d, e.Prev),
			"A deed before it was changed or replaced. Get a fresh export."
	case CodeChainOrgMismatch:
		return fmt.Sprintf("%s belongs to %s, not %s.", d, orDash(e.Org), orDash(e.WantOrg)),
			"Verify each org's deeds separately."
	case CodeChainTimeRegression:
		return fmt.Sprintf("%s was sealed before deed %d.", d, e.Prev),
			fmt.Sprintf("Treat the chain as altered from %s.", strings.ToLower(d[:1])+d[1:])
	}
	return fmt.Sprintf("%s failed verification (%s).", d, e.Code), "Export it again from the issuer."
}

// deedRef is "Deed 812", or "The deed" when the seq is unknown.
func deedRef(seq int64) string {
	if seq <= 0 {
		return "The deed"
	}
	return fmt.Sprintf("Deed %d", seq)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
