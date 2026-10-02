package witness

// Console and CLI copy for the Witness (S25 renders it; brand §3, §12). One catalog, so the
// Console, the CLI and the docs say the same words.
const (
	TooltipL0 = "L0 Reported: the tool's own answer."
	TooltipL1 = "L1 Confirmed: Deedglass read the result back from the target."
	TooltipL2 = "L2 Witnessed: Deedglass saw the change in the system of record."

	// PanelNoReadBack is the Witness panel for a write tool without a read-back; %s are the
	// connector and the tool.
	PanelNoReadBack = "No read-back for this tool yet. Add one in Connectors › %s › %s so Deedglass can confirm writes."
)

// Tooltip is a level's one-line explanation.
func Tooltip(l Level) string {
	switch l {
	case L1:
		return TooltipL1
	case L2:
		return TooltipL2
	}
	return TooltipL0
}

// Summary is the one sentence a support engineer can read aloud about a decision: the first
// violation for a MISMATCH; the strongest pass at the claimed level for a VERIFIED; for an
// UNVERIFIABLE, why the claim stopped where it did.
func Summary(d Decision) string {
	first := func(match func(Reason) bool) string {
		for _, r := range d.Reasons {
			if match(r) {
				return r.Message
			}
		}
		return ""
	}
	switch d.Verdict {
	case Mismatch:
		// L2's violations in S21's order: the count the agent got wrong says the most.
		if d.Level == L2 {
			for _, c := range L2SummaryOrder {
				if s := first(func(r Reason) bool { return r.Code == c && r.Level == L2 }); s != "" {
					return s
				}
			}
		}
		if s := first(func(r Reason) bool { return r.Class == Violation && r.Level == d.Level }); s != "" {
			return s
		}
		return first(func(r Reason) bool { return r.Class == Violation })
	case Verified:
		// The most specific pass: a read-back over the bare "the tool reported success".
		for _, c := range []Code{CodeRowWritten, CodeReadbackMatched, CodePostconditionHeld, CodeResponseSchemaValid, CodeEdgeReportedSuccess, CodeToolReportedSuccess} {
			if s := first(func(r Reason) bool { return r.Code == c && r.Level == d.Level }); s != "" {
				return s
			}
		}
		return first(func(r Reason) bool { return r.Class == Pass && r.Level == d.Level })
	}
	for _, c := range []Code{CodeLevelBelowRequired, CodeNoChecks} {
		if s := first(func(r Reason) bool { return r.Code == c }); s != "" {
			return s
		}
	}
	if s := first(func(r Reason) bool { return r.Class == Gap && r.Level == d.Level }); s != "" {
		return s
	}
	return first(func(r Reason) bool { return r.Class == Gap })
}

// L2SummaryOrder is the order in which L2's violations become a MISMATCH's summary (S21 §3.6):
// the first one present is the sentence a support engineer reads aloud.
var L2SummaryOrder = []Code{CodeRowCountExceeded, CodeUnexpectedTable, CodeUnexpectedOperation, CodeMissingChange,
	CodeUnexpectedKey, CodeUnexpectedColumn, CodeValueMismatch, CodeRowLimitExceeded, CodeMultipleTransactions,
	CodeLedgerMismatch, CodeMarkerCount, CodeMarkerMismatch}
