package decoder

// Classify sets f's class and action from what the frame holds (S20 §3.7; ADR-003 C0–C5 in
// Deedglass names). It is pure, and uses only the frame's contents: never the peek's xid or LSN
// order (ADR-002 C), and never the marker's LSN. org is the connector's org; pubChanged is true
// when this peek saw the publication change or Postgres 18 skip it ("skipped loading
// publication"). It returns false for C0 (nothing to keep: no row change, ledger row or marker).
//
// Let W be the row changes outside the ledger's own inserts, L the ledger inserts and M the
// transactional deedglass markers:
//
//	W=∅, L=∅, M=∅               → ignored (C0)
//	W≠∅, L=∅, M=∅               → unattributed (C1)
//	M≠∅, L=∅                    → forged_marker (C2), or publication_gap when pubChanged
//	L≠∅, a row of another org   → foreign_ledger
//	L≠∅, a row that didn't parse → marker_mismatch, flag ledger_invalid
//	|L|>1 or |M|≠1              → marker_count (C4), naming the action when every row names one
//	|L|=1, |M|=1, contents differ → marker_mismatch (C4)
//	low32(L.xid8) ≠ BEGIN.xid   → marker_mismatch, flag ledger_xid_mismatch
//	otherwise                   → bound (C5)
//
// The decoder never checks the action against the Gate's record (S21 does) and never decides a
// verdict.
func Classify(f *Frame, org string, pubChanged bool) bool {
	w, l, m := f.Total, len(f.Ledger), len(f.Markers)
	if pubChanged {
		f.flag("publication_changed")
	}
	switch {
	case w == 0 && l == 0 && m == 0:
		return false
	case l == 0 && m == 0:
		f.Class = ClassUnattributed
		return true
	case l == 0:
		if id := firstMarkerAction(f.Markers); pubChanged && id != "" {
			f.Class, f.ActionID = ClassPublicationGap, id
			return true
		}
		f.Class = ClassForgedMarker
		return true
	}
	for _, r := range f.Ledger {
		if r.Invalid == "" && r.OrgID != org {
			f.Class, f.ActionID = ClassForeignLedger, ""
			return true
		}
	}
	for _, r := range f.Ledger {
		if r.Invalid != "" {
			f.Class, f.ActionID = ClassMarkerMismatch, ""
			if m == 1 {
				f.ActionID = firstMarkerAction(f.Markers)
			}
			f.flag("ledger_invalid")
			return true
		}
	}
	if l > 1 || m != 1 {
		f.Class, f.ActionID = ClassMarkerCount, ""
		if one := oneAction(f.Ledger); one != "" {
			f.ActionID = one
		}
		return true
	}
	row := f.Ledger[0]
	f.ActionID = row.ActionID
	if f.Markers[0] != row.ActionID {
		f.Class = ClassMarkerMismatch
		return true
	}
	if low, ok := row.XIDLow32(); !ok || low != f.Xid {
		f.Class = ClassMarkerMismatch
		f.flag("ledger_xid_mismatch")
		return true
	}
	f.Class = ClassBound
	return true
}

// firstMarkerAction is the first marker that names an action, or "".
func firstMarkerAction(ms []string) string {
	for _, x := range ms {
		if _, ok := ParseMarker([]byte(x)); ok {
			return x
		}
	}
	return ""
}

// oneAction is the action every ledger row names, or "" when they differ.
func oneAction(rows []LedgerRow) string {
	id := ""
	for _, r := range rows {
		if id == "" {
			id = r.ActionID
		} else if r.ActionID != id {
			return ""
		}
	}
	return id
}

// NonTxFrame is the stored frame for a non-transactional deedglass message that isn't a valid
// heartbeat (C3): its commit LSN is the message's LSN.
func NonTxFrame(msg NonTxMessage) *Frame {
	return &Frame{CommitLSN: msg.LSN, EndLSN: msg.LSN, Class: ClassForgedMarkerNonTx, NonTx: true,
		Markers: []string{markerEvidence(msg.Content)}, Tables: map[string]TableInfo{}, Counts: map[string]OpCounts{},
		Digest: newDigester().sum()}
}
