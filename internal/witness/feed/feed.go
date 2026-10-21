package feed

import (
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// Source names a foreign change feed: what carried it and which database wrote it.
type Source struct {
	Via      string `json:"via"`      // "Debezium"
	Database string `json:"database"` // "MySQL"
	Method   string `json:"method"`   // the level's method in the record: "debezium.mysql"
}

// Label is the level as a person reads it: "Proven · via Debezium (MySQL)".
func (s Source) Label() string {
	return witness.PlainL2 + " · via " + s.Via + " (" + s.Database + ")"
}

// Phrase names the feed inside a sentence: "Debezium feed from MySQL".
func (s Source) Phrase() string { return s.Via + " feed from " + s.Database }

// Tx is one committed transaction as a feed carried it. Changes, Counts and Tables have the shapes
// witness/l2 reads; the ledger table's rows are in Ledger, not in Changes or Counts.
type Tx struct {
	ID         string // the feed's transaction id (a MySQL GTID, a Postgres txId)
	Position   string // where the source logged it ("mysql-bin.000003:10824"); evidence only
	ServerID   string // the source server, when the feed names it
	CommitTime time.Time
	Changes    []l2.Change
	Counts     map[string]l2.OpCounts
	Tables     map[string]l2.TableInfo
	Ledger     []l2.LedgerRow
	Total      int    // row changes, the ledger's aside
	Digest     string // "sha256:" over the transaction's events as the feed carried them
	// Incomplete says why the feed holds only part of the transaction ("" when it holds all of it).
	Incomplete string
}

// touches reports whether the transaction changed table t.
func (t *Tx) touches(table string) bool {
	c := t.Counts[table]
	return c.Rows() > 0 || c.Truncate > 0
}

// frame is the transaction as witness/l2's Frame, with class.
func (t *Tx) frame(class string) l2.Frame {
	return l2.Frame{ID: t.ID, Class: class, SystemID: t.ServerID, CommitLSN: t.Position, CommitTime: t.CommitTime,
		Ledger: t.Ledger, Tables: t.Tables, Counts: t.Counts, Changes: t.Changes, Total: t.Total, Digest: t.Digest}
}

// Attributions: how the compared transactions were tied to the action.
const (
	AttrAction    = "action"     // a ledger row in the transaction names the action
	AttrNamed     = "named"      // the caller named the transaction (--tx); any writer
	AttrAnyWriter = "any_writer" // the transactions that touch the action's tables; any writer
	AttrNone      = "none"       // nothing matched
)

// Input is one evaluation: the feed's transactions, what the Gate authorized, the statement and
// its reported result, and the connector-style settings L2 reads.
type Input struct {
	Source  Source
	Txs     []Tx
	Action  l2.Action
	Derive  l2.DeriveInput // Tool, SQL, Params, DefaultSchema, Reported
	Config  l2.Config
	Tx      string // a transaction id the caller names; "" to bind by ledger row or by table
	Witness time.Time
}

// Report is the result: the decision, its witness_detail, how the transactions were tied to the
// action, and which transactions were read.
type Report struct {
	Source      Source
	Attribution string
	Txs         []*Tx // the transactions compared, oldest first
	Outcome     witness.Outcome
	Result      l2.Result
}

// Evaluate binds the feed's transactions to the action, compares them with what the statement
// should have done, and decides at L2 with the source's method.
func Evaluate(in Input) (Report, error) {
	rep := Report{Source: in.Source}
	txs, attr := bind(in)
	rep.Attribution, rep.Txs = attr, txs
	if attr == AttrAction {
		in.Action = fillUnasserted(in.Action, txs)
	}
	feedVar := map[string]any{"feed": in.Source.Phrase()}

	var res l2.Result
	var rs []witness.Reason
	switch {
	case len(txs) == 0:
		res = l2.Gap(witness.CodeFeedNoMatch, feedVar)
		rs = res.Reasons
	default:
		var gaps []witness.Reason
		frames := make([]l2.Frame, 0, len(txs))
		for _, t := range txs {
			if t.Incomplete != "" {
				gaps = append(gaps, witness.R(witness.L2, witness.CodeFeedIncomplete, "feed",
					map[string]any{"feed": in.Source.Phrase(), "tx": t.ID, "why": t.Incomplete}))
				continue
			}
			frames = append(frames, t.frame(classOf(t, attr)))
		}
		if len(gaps) > 0 {
			// Part of a transaction is never compared: a cut or replayed feed can't become a
			// violation or a pass (rule 3).
			res = l2.Result{Reasons: gaps}
			if len(frames) > 0 {
				res.Evidence = l2.Evaluate(l2.Input{Action: in.Action, Frames: frames, Derive: in.Derive, Config: in.Config}).Evidence
			}
		} else {
			res = l2.Evaluate(l2.Input{Action: in.Action, Frames: frames, Derive: in.Derive, Config: in.Config})
		}
		rs = res.Reasons
		if attr != AttrAction {
			rs = append(rs, witness.R(witness.L2, witness.CodeAnyWriter, "attribution", feedVar))
		}
	}
	for i := range rs {
		rs[i] = Relabel(rs[i], in.Source)
	}
	res.Reasons, res.Method = rs, in.Source.Method
	if res.Evidence != nil {
		ev := *res.Evidence
		if ev.Transaction != nil {
			tr := *ev.Transaction
			ev.Transaction = &tr
		}
		res.Evidence = &ev
	}
	rep.Result = res
	methods := map[witness.Level]string{witness.L2: in.Source.Method}
	d := witness.Decide(witness.L2, map[witness.Level][]witness.Reason{witness.L2: rs}, methods)
	at := in.Witness
	if at.IsZero() {
		at = time.Now()
	}
	out, err := witness.FinishWith(d, methods, witness.Extras{L2: res.Evidence}, at, 0)
	if err != nil {
		return rep, err
	}
	rep.Outcome = out
	return rep, nil
}

// fillUnasserted takes the org and agent the caller didn't give from the ledger row that names the
// action, so the cross-check compares only what the caller asserted.
func fillUnasserted(a l2.Action, txs []*Tx) l2.Action {
	for _, t := range txs {
		for _, lr := range t.Ledger {
			if lr.ActionID != a.ID {
				continue
			}
			if a.Org == "" {
				a.Org = lr.OrgID
			}
			if a.Agent == "" {
				a.Agent = lr.AgentID
			}
			return a
		}
	}
	return a
}

// classOf is the frame class a compared transaction gets: bound, or marker_count when its ledger
// holds more than one row.
func classOf(t *Tx, attr string) string {
	if attr == AttrAction && len(t.Ledger) > 1 {
		return l2.ClassMarkerCount
	}
	return l2.ClassBound
}

// bind picks the transactions to compare (S21's rules over a feed, ADR-114 §2).
func bind(in Input) ([]*Tx, string) {
	all := make([]*Tx, 0, len(in.Txs))
	for i := range in.Txs {
		all = append(all, &in.Txs[i])
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CommitTime.Before(all[j].CommitTime) })
	if in.Action.ID != "" {
		var named []*Tx
		for _, t := range all {
			for _, lr := range t.Ledger {
				if lr.ActionID == in.Action.ID {
					named = append(named, t)
					break
				}
			}
		}
		if len(named) > 0 {
			return named, AttrAction
		}
	}
	if in.Tx != "" {
		for _, t := range all {
			if t.ID == in.Tx {
				return []*Tx{t}, AttrNamed
			}
		}
		return nil, AttrNone
	}
	tables := footprintTables(in, all)
	var out []*Tx
	for _, t := range all {
		if len(t.Ledger) > 0 {
			continue // a transaction another action's ledger row claims is that action's
		}
		for _, tb := range tables {
			if t.touches(tb) {
				out = append(out, t)
				break
			}
		}
	}
	if len(out) == 0 {
		return nil, AttrNone
	}
	return out, AttrAnyWriter
}

// footprintTables are the tables the statement names (l2.Derive's footprint), or none when it
// can't say: then nothing is compared by table, and the caller names the transaction with --tx.
func footprintTables(in Input, all []*Tx) []string {
	d := in.Derive
	d.PK = map[string][]string{}
	for _, t := range all {
		for name, ti := range t.Tables {
			if len(ti.PK) > 0 {
				d.PK[name] = ti.PK
			}
		}
	}
	fp, _ := l2.Derive(d)
	var out []string
	for _, te := range fp.Tables {
		out = append(out, te.Table)
	}
	return out
}

// Relabel says the source instead of Postgres in witness/l2's sentences: "Postgres changed 300 rows"
// becomes "MySQL changed 300 rows", "Postgres's change log" becomes "the Debezium feed from MySQL".
func Relabel(r witness.Reason, s Source) witness.Reason {
	if s.Database == "" {
		return r
	}
	m := r.Message
	if rest, ok := strings.CutPrefix(m, "Postgres's change log"); ok {
		m = "The " + s.Phrase() + rest
	}
	m = strings.ReplaceAll(m, "Postgres's change log", "the "+s.Phrase())
	m = strings.ReplaceAll(m, "Postgres", s.Database)
	r.Message = m
	return r
}
