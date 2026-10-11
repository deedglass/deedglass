package l2

import (
	"sort"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
)

// Method is L2's method name (contract §6, the deed's witness.method).
const Method = "postgres.pgoutput"

// Action is what the Gate authorized, for the ledger cross-check (S21 §3.5).
type Action struct {
	ID         string
	Org        string
	Agent      string
	IntentHash *string
	// SQLDigest is the digest of the authorized SQL with S17's exact recipe (postgres.execute:
	// SQLDigest(sql, params); postgres.call: CallDigest(function, args)); "" when it can't be
	// computed, and then the field isn't compared.
	SQLDigest string
	// PlanDigest is an undo's args.plan_digest (S23): the undo passes it to begin_action.
	PlanDigest string
}

// Input is one L2 evaluation: the frames that name the action, the statement and its result, and
// what the connector knows about the tables.
type Input struct {
	Action   Action
	Frames   []Frame // naming the action, oldest first: bound, marker_count, marker_mismatch, publication_gap
	Derive   DeriveInput
	Config   Config
	Triggers map[string][]string // footprint table → enabled user triggers; nil when not looked up
}

// Result is L2's reasons and its evidence for levels.L2.
type Result struct {
	Reasons   []witness.Reason
	Evidence  *witness.L2Evidence
	Footprint Footprint
}

// HasFrame reports whether the result read a frame (and so is evidence, not a timeout or a lost
// slot).
func (r Result) HasFrame() bool { return r.Evidence != nil && r.Evidence.Transaction != nil }

// Evaluate is L2 for one action with at least one frame naming it: the frame classes, the ledger
// cross-check, the footprint and the comparison, the trigger note, and the diff. Every rule runs
// and every finding is listed.
func Evaluate(in Input) Result {
	var rows, gaps []*Frame
	for i := range in.Frames {
		f := &in.Frames[i]
		switch f.Class {
		case ClassBound, ClassMarkerCount, ClassMarkerMismatch:
			rows = append(rows, f)
		case ClassPublicationGap:
			gaps = append(gaps, f)
		}
	}
	var rs []witness.Reason
	add := func(code witness.Code, check string, vars map[string]any) {
		rs = append(rs, witness.R(witness.L2, code, check, vars))
	}
	ids := make([]string, 0, len(in.Frames))
	for _, f := range in.Frames {
		if f.ID != "" {
			ids = append(ids, f.ID)
		}
	}
	sort.Strings(ids)
	if len(rows) == 0 {
		if len(gaps) == 0 {
			return Result{}
		}
		add(witness.CodePublicationGap, "publication", nil)
		ev := BuildDiff(Footprint{Derivation: DerivationUnknown, Why: "publication_gap"}, gaps[0], in.Config)
		ev.Expected, ev.Rows, ev.RowsShown, ev.RowsMore = nil, []witness.L2Row{}, 0, 0
		ev.Frames, ev.FrameIDs = len(in.Frames), ids
		return Result{Reasons: rs, Evidence: ev}
	}
	if len(rows) > 1 {
		add(witness.CodeMultipleTransactions, "frames", map[string]any{"n": len(rows)})
	}
	// The frame the comparison reads: the first bound one, else the first that names the action.
	primary := rows[0]
	for _, f := range rows {
		if f.Class == ClassBound {
			primary = f
			break
		}
	}
	for _, f := range rows {
		switch f.Class {
		case ClassMarkerCount:
			add(witness.CodeMarkerCount, "marker", map[string]any{"ledger": len(f.Ledger), "markers": len(f.Markers)})
		case ClassMarkerMismatch:
			why := "the marker names another action"
			if f.HasFlag("ledger_xid_mismatch") {
				why = "the ledger row names another transaction"
			}
			add(witness.CodeMarkerMismatch, "marker", map[string]any{"why": why})
		}
		rs = append(rs, crossCheck(in.Action, f)...)
	}
	in.Derive.PK = map[string][]string{}
	for t, ti := range primary.Tables {
		if len(ti.PK) > 0 {
			in.Derive.PK[t] = ti.PK
		}
	}
	fp, drs := Derive(in.Derive)
	rs = append(rs, drs...)
	if fp.Derivation != DerivationUnknown {
		rs = append(rs, Compare(fp, primary, in.Config)...)
	} else {
		// Nothing to compare against, but a connector's max_rows still holds.
		rs = append(rs, RowLimits(primary, in.Config)...)
	}
	for _, te := range fp.Tables {
		names := in.Triggers[te.Table]
		if len(names) == 0 {
			continue
		}
		add(witness.CodeTriggersPresent, "triggers", map[string]any{"table": te.Table, "names": joinNames(names)})
		if in.Config.StrictTriggers {
			add(witness.CodeTriggersUnwatched, "triggers", map[string]any{"table": te.Table})
		}
	}
	ev := BuildDiff(fp, primary, in.Config)
	ev.Frames, ev.FrameIDs = len(in.Frames), ids
	return Result{Reasons: rs, Evidence: ev, Footprint: fp}
}

func joinNames(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// crossCheck compares a frame's ledger rows that name the action with what the Gate authorized
// (S21 §3.5). A difference is ledger_mismatch naming the field, never the values.
func crossCheck(a Action, f *Frame) []witness.Reason {
	var rs []witness.Reason
	for _, lr := range f.Ledger {
		if lr.ActionID != a.ID {
			continue
		}
		var fields []string
		if lr.Invalid != "" {
			fields = append(fields, "row")
		}
		if lr.OrgID != a.Org {
			fields = append(fields, "org_id")
		}
		if lr.AgentID != a.Agent {
			fields = append(fields, "agent_id")
		}
		switch {
		case a.PlanDigest != "":
			if lr.SQLDigest != a.PlanDigest {
				fields = append(fields, "sql_digest")
			}
		case a.SQLDigest != "":
			if lr.SQLDigest != a.SQLDigest {
				fields = append(fields, "sql_digest")
			}
		}
		if !sameOpt(lr.IntentHash, a.IntentHash) {
			fields = append(fields, "intent_hash")
		}
		if len(fields) > 0 {
			r := witness.R(witness.L2, witness.CodeLedgerMismatch, "ledger", map[string]any{"field": joinNames(fields)}).
				WithEvidence(map[string]any{"fields": fields})
			rs = append(rs, r)
		}
	}
	return rs
}

func sameOpt(a, b *string) bool {
	if a == nil || b == nil {
		return (a == nil || *a == "") && (b == nil || *b == "")
	}
	return *a == *b
}

// Gap is a gap-only L2 result (no frame): l2_timeout, slot_dropped, decoder_halted, no_evidence.
func Gap(code witness.Code, vars map[string]any) Result {
	return Result{Reasons: []witness.Reason{witness.R(witness.L2, code, "evidence", vars)}}
}

// Timeout is l2_timeout for a wait of d.
func Timeout(d time.Duration) Result {
	return Gap(witness.CodeL2Timeout, map[string]any{"timeout": durationWords(d)})
}

func durationWords(d time.Duration) string {
	if d%time.Second == 0 {
		return d.String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// Decoder status reasons (decoder.Reason*) as they reach SlotLost; only the names L2 maps.
const (
	lostSlotInvalidated = "slot_invalidated"
	lostSlotMissing     = "slot_missing"
	lostDropped         = "dropped"
	lostDeleted         = "connector_deleted"
)

// Lost maps a decoder's SlotLost reason (or a halted status at plan time) to L2's gap:
// slot_invalidated, slot_dropped, or decoder_halted naming the reason.
func Lost(reason string) Result {
	switch reason {
	case lostSlotInvalidated:
		return Gap(witness.CodeSlotInvalidated, map[string]any{"why": reason})
	case lostSlotMissing, lostDropped, lostDeleted:
		return Gap(witness.CodeSlotDropped, nil).withEvidence(map[string]any{"reason": reason})
	}
	return Gap(witness.CodeDecoderHalted, map[string]any{"why": reason})
}

func (r Result) withEvidence(ev map[string]any) Result {
	for i := range r.Reasons {
		r.Reasons[i] = r.Reasons[i].WithEvidence(ev)
	}
	return r
}

// NoEvidence is reconcile's verdict on a ledger row with no frame.
func NoEvidence() Result { return Gap(witness.CodeNoEvidence, nil) }

// Console copy for S25 (brand §12; one catalog so the Console, the CLI and the docs agree).
const (
	CopyExpected      = "Expected"
	CopyObserved      = "Observed"
	CopyBadgeExpected = "expected"
	CopyBadgeUnexpect = "unexpected"
	// CopyFooter: %s is the commit LSN, %s the time ("2026-10-07 13:12:09 UTC").
	CopyFooter = "Proven by Postgres · commit %s · %s"
	// CopyTruncated: %s shown, %s total ("Showing the first 20 of 301 changed rows.").
	CopyTruncated       = "Showing the first %s of %s changed rows."
	CopyAmendmentBanner = "This action has a later verdict. The original record is unchanged; the amendment shows what Postgres showed afterwards."
	CopyWitnessOff      = "Turn on checks to compare what the agent reported with what Postgres committed."
	CopyKeepsWatching   = "Deedglass keeps watching. If Postgres's change log shows it later, an amendment record holds the result."
)
