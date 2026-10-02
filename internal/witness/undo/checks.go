package undo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Status is a check's outcome: pass ✓, warn ○, fail ✕, or skip (it couldn't run because an earlier
// check failed).
type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Check is one safety check as the API and the CLI show it (the connector_test check shape).
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	FixSQL string `json:"fix_sql,omitempty"`
	DocURL string `json:"doc_url,omitempty"`
}

// Available reports whether no check failed (S23 §3.3: available = no fail).
func Available(cs []Check) bool {
	for _, c := range cs {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}

// Failed lists the ids of the failing checks.
func Failed(cs []Check) []string {
	var out []string
	for _, c := range cs {
		if c.Status == StatusFail {
			out = append(out, c.ID)
		}
	}
	return out
}

// ActionFacts is what the original action's row says.
type ActionFacts struct {
	ID            string
	Sealed        bool
	Mode          string // gateway | proxy
	Tool          string
	WitnessLevel  string // L0 | L1 | L2 | ""
	Verdict       string
	SealedAt      time.Time
	PayloadPruned *time.Time
	Executed      bool // execution succeeded or unknown (something may have changed)
}

// ConnectorFacts is the connector the action ran through, as it is now.
type ConnectorFacts struct {
	Exists         bool
	Name           string
	Type           string
	Status         string
	HasCredentials bool
	WitnessEnabled bool
}

// OtherUndo is another undo of the same action that holds the one-live-undo slot.
type OtherUndo struct {
	ID    string
	State string // pending | running | applied
	At    string // when it applied, in the org's words ("10:14 IST")
}

// CheckInput is everything the twelve checks read.
type CheckInput struct {
	Action        ActionFacts
	Connector     ConnectorFacts
	Now           time.Time
	MaxAge        time.Duration
	MaxRows       int
	Other         *OtherUndo
	Evidence      *Evidence
	Plan          *Plan
	Findings      []Finding // Build's: evidence, primary_key, columns
	Catalog       *Catalog
	Current       Current // nil: not read (the plan didn't get that far)
	AllowTriggers bool
}

// Checks runs the twelve checks in order (S23 §3.3). Every failing check is listed with its fix.
func Checks(in CheckInput) []Check {
	by := map[string][]Finding{}
	for _, f := range in.Findings {
		by[f.Check] = append(by[f.Check], f)
	}
	out := make([]Check, 0, len(CheckOrder))
	add := func(id string, st Status, detail, fix, fixSQL string) {
		c := Check{ID: id, Title: CheckTitles[id], Status: st, Detail: detail, Fix: fix, FixSQL: fixSQL}
		if st == StatusFail || st == StatusWarn {
			c.DocURL = DocURL
		}
		out = append(out, c)
	}
	fromFindings := func(id string, pass string) {
		fs := by[id]
		if len(fs) == 0 {
			add(id, StatusPass, pass, "", "")
			return
		}
		st := StatusWarn
		var texts, sqls []string
		for _, f := range fs {
			if !f.Warn {
				st = StatusFail
			}
			texts = append(texts, f.Text)
			if f.FixSQL != "" && !slices.Contains(sqls, f.FixSQL) {
				sqls = append(sqls, f.FixSQL)
			}
		}
		fix := fs[0].Fix
		if fix == "" && st == StatusFail {
			fix = FixFailing
		}
		add(id, st, strings.Join(texts, " "), fix, strings.Join(sqls, "\n"))
	}
	p := in.Plan
	// 1. evidence
	switch {
	case !in.Action.Sealed:
		by[CheckEvidence] = append([]Finding{{Check: CheckEvidence, Text: MsgNotSealed}}, by[CheckEvidence]...)
	case !in.Action.Executed:
		by[CheckEvidence] = append([]Finding{{Check: CheckEvidence, Text: MsgNotWritten}}, by[CheckEvidence]...)
	case in.Action.WitnessLevel != "L2" && in.Evidence != nil && len(in.Evidence.Problems) > 0 && in.Evidence.Problems[0].Text == ProblemNoFrame.Text:
		by[CheckEvidence] = []Finding{ProblemNotWitnessedAt(in.Action.WitnessLevel)}
	}
	if p != nil && p.Rows == 0 && len(by[CheckEvidence]) == 0 {
		by[CheckEvidence] = append(by[CheckEvidence], Finding{Check: CheckEvidence, Text: MsgNothing})
	}
	evPass := ""
	if in.Evidence != nil {
		evPass = fmt.Sprintf("%s · %s · %s row images · xid %d", nonEmpty(in.Action.WitnessLevel, "L2"), nonEmpty(in.Action.Verdict, "sealed"),
			thousands(len(in.Evidence.Changes)), in.Evidence.Ref.Xid)
	}
	fromFindings(CheckEvidence, evPass)
	// 2. connector
	c := in.Connector
	switch {
	case in.Action.Mode == "proxy":
		add(CheckConnector, StatusFail, MsgConnectorProxy, FixFailing, "")
	case !c.Exists:
		add(CheckConnector, StatusFail, fmt.Sprintf(MsgConnectorDeleted, c.Name), FixFailing, "")
	case c.Type != "postgres":
		add(CheckConnector, StatusFail, fmt.Sprintf(MsgConnectorNotPG, nonEmpty(c.Type, "a connector without L2")), FixFailing, "")
	case c.Status != "active":
		add(CheckConnector, StatusFail, fmt.Sprintf(MsgConnectorInactive, c.Name, c.Status), FixFailing, "")
	case !c.HasCredentials:
		add(CheckConnector, StatusFail, fmt.Sprintf(MsgConnectorNoCreds, c.Name), FixFailing, "")
	case !c.WitnessEnabled:
		add(CheckConnector, StatusFail, fmt.Sprintf(MsgConnectorNoWitness, c.Name), FixFailing, "")
	default:
		add(CheckConnector, StatusPass, fmt.Sprintf(MsgConnectorOK, c.Name), "", "")
	}
	// 3. age
	age := in.Now.Sub(in.Action.SealedAt)
	switch {
	case in.Action.PayloadPruned != nil:
		add(CheckAge, StatusFail, fmt.Sprintf(MsgAgePruned, in.Action.PayloadPruned.UTC().Format("2006-01-02")), FixFailing, "")
	case !in.Action.Sealed:
		add(CheckAge, StatusSkip, "", "", "")
	case in.MaxAge > 0 && age > in.MaxAge:
		add(CheckAge, StatusFail, fmt.Sprintf(MsgAgeTooOld, DurationWords(age), DurationWords(in.MaxAge)), FixFailing, "")
	case in.MaxAge <= 0:
		add(CheckAge, StatusPass, sealedAgo(age), "", "")
	default:
		add(CheckAge, StatusPass, sealedAgo(age)+" · limit "+DurationWords(in.MaxAge), "", "")
	}
	// 4. not_undone
	switch o := in.Other; {
	case o == nil:
		add(CheckNotUndone, StatusPass, "", "", "")
	case o.State == "applied":
		add(CheckNotUndone, StatusFail, fmt.Sprintf(MsgUndoneBy, ShortID(o.ID), o.At), period("To put it back, preview an undo of "+ShortID(o.ID)), "")
	case o.State == "running":
		add(CheckNotUndone, StatusFail, period(fmt.Sprintf(strings.TrimSuffix(MsgUndoRunning, "."), ShortID(o.ID))), "Wait for it to finish, then preview again.", "")
	default:
		add(CheckNotUndone, StatusFail, period(fmt.Sprintf(strings.TrimSuffix(MsgUndoPending, "."), ShortID(o.ID))), "Countersign or cancel it first.", "")
	}
	// 5. row_limit
	switch {
	case p == nil:
		add(CheckRowLimit, StatusSkip, "", "", "")
	case in.MaxRows > 0 && p.Rows > in.MaxRows:
		add(CheckRowLimit, StatusFail, fmt.Sprintf(MsgRowLimit, thousands(p.Rows), thousands(in.MaxRows)), "", "")
	case in.MaxRows <= 0:
		add(CheckRowLimit, StatusPass, rowsText(p.Rows), "", "")
	default:
		add(CheckRowLimit, StatusPass, fmt.Sprintf(MsgRowLimitOK, thousands(p.Rows), thousands(in.MaxRows)), "", "")
	}
	if p == nil || in.Catalog == nil {
		// The catalog checks need a plan; Build already reported why there is none.
		fromFindingsOrSkip := func(id string) {
			if len(by[id]) > 0 {
				fromFindings(id, "")
				return
			}
			add(id, StatusSkip, "", "", "")
		}
		for _, id := range []string{CheckReplicaIdentity, CheckPrimaryKey, CheckColumns, CheckTriggers, CheckCascades, CheckUnchanged, CheckPrivileges} {
			fromFindingsOrSkip(id)
		}
		return out
	}
	tables := planTables(p, in.Catalog)
	// 6. replica_identity
	var notFull []*TableCatalog
	for _, tc := range tables {
		if tc.ReplIdent != "f" {
			notFull = append(notFull, tc)
		}
	}
	if len(notFull) == 0 {
		add(CheckReplicaIdentity, StatusPass, "full on "+tablesText(p.TableNames()), "", "")
	} else {
		var names, sqls []string
		for _, tc := range notFull {
			names = append(names, fmt.Sprintf("%s is %s", tc.String(), identityWord(tc.ReplIdent)))
			sqls = append(sqls, fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY FULL;", fixName(tc.Schema, tc.Name)))
		}
		add(CheckReplicaIdentity, StatusFail, "Replica identity "+strings.Join(names, "; ")+", so the undo itself couldn't be witnessed.", FixReplica, strings.Join(sqls, "\n"))
	}
	// 7. primary_key, 8. columns (Build decided them)
	var pks []string
	for _, t := range p.Tables {
		pks = append(pks, fmt.Sprintf("%s (%s)", t.String(), joinComma(t.PK)))
	}
	fromFindings(CheckPrimaryKey, strings.Join(pks, "; "))
	fromFindings(CheckColumns, "unchanged since the action")
	// 9. triggers
	var trig []string
	var trigSQL []string
	for _, tc := range tables {
		for _, t := range tc.Triggers {
			trig = append(trig, fmt.Sprintf("%s has trigger %s.", tc.String(), t))
			trigSQL = append(trigSQL, fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER %s;", fixName(tc.Schema, tc.Name), quoteIdent(t)))
		}
		for _, r := range tc.Rules {
			trig = append(trig, fmt.Sprintf("%s has rule %s.", tc.String(), r))
			trigSQL = append(trigSQL, fmt.Sprintf("ALTER TABLE %s DISABLE RULE %s;", fixName(tc.Schema, tc.Name), quoteIdent(r)))
		}
	}
	switch {
	case len(trig) == 0:
		add(CheckTriggers, StatusPass, "No enabled triggers on "+tablesText(p.TableNames())+".", "", "")
	case in.AllowTriggers:
		add(CheckTriggers, StatusWarn, strings.Join(trig, " ")+" The undo fires it again; the witness shows what it did.", "", "")
	default:
		add(CheckTriggers, StatusFail, strings.Join(trig, " ")+" "+FixTriggers, FixAllowTrig, strings.Join(trigSQL, "\n"))
	}
	// 10. cascades
	var casc []string
	for _, tc := range tables {
		deletes, updated := stepFacts(p, tc.RelID)
		for _, fk := range tc.Referrers {
			switch {
			case deletes != nil && Acts(fk.OnDelete):
				casc = append(casc, fmt.Sprintf("%s references %s ON DELETE %s. Deleting %s %s would change its rows in %s too.",
					fk.From, tc.String(), fkWord(fk.OnDelete), tc.String(), KeyText(deletes, sortedCols(deletes)), fk.From))
			case Acts(fk.OnUpdate) && anyIn(fk.Columns, updated):
				casc = append(casc, fmt.Sprintf("%s references %s ON UPDATE %s. Putting back %s would change its rows in %s too.",
					fk.From, tc.String(), fkWord(fk.OnUpdate), joinComma(fk.Columns), fk.From))
			}
		}
	}
	if len(casc) == 0 {
		add(CheckCascades, StatusPass, "no cascading foreign keys", "", "")
	} else {
		add(CheckCascades, StatusFail, strings.Join(casc, " "), "Undo refuses a step that would change rows it didn't plan.", "")
	}
	// 11. unchanged
	if in.Current == nil {
		add(CheckUnchanged, StatusSkip, "", "", "")
	} else {
		bad, total := Compare(p, in.Current)
		if len(bad) == 0 {
			add(CheckUnchanged, StatusPass, fmt.Sprintf("%s of %s rows still hold what the action wrote", thousands(total), thousands(total)), "", "")
		} else {
			d := bad[0].Text
			switch more := len(bad) - 1; {
			case more == 1:
				d += " 1 more row differs."
			case more > 1:
				d += fmt.Sprintf(" %s more rows differ.", thousands(more))
			}
			add(CheckUnchanged, StatusFail, d, FixRowsChanged, "")
		}
	}
	// 12. privileges
	var missing, can []string
	var grants []string
	var rls []string
	for _, tc := range tables {
		need := []string{"SELECT"}
		if p.countOn(tc.RelID, OpUpdate) > 0 {
			need = append(need, "UPDATE")
		}
		if p.countOn(tc.RelID, OpInsert) > 0 {
			need = append(need, "INSERT")
		}
		if p.countOn(tc.RelID, OpDelete) > 0 {
			need = append(need, "DELETE")
		}
		have := map[string]bool{"SELECT": tc.Select, "UPDATE": tc.Update, "INSERT": tc.Insert, "DELETE": tc.Delete}
		var lack []string
		for _, n := range need {
			if !have[n] {
				lack = append(lack, n)
			}
		}
		if len(lack) > 0 {
			missing = append(missing, fmt.Sprintf("%s can't %s %s.", nonEmpty(in.Catalog.Role, "deedglass_agent"), joinAnd(lack), tc.String()))
			grants = append(grants, fmt.Sprintf("GRANT %s ON %s TO deedglass_agent;", joinComma(lack), fixName(tc.Schema, tc.Name)))
		} else {
			can = append(can, fmt.Sprintf("%s %s", joinAnd(need), tc.String()))
		}
		if tc.RLS && !in.Catalog.BypassRLS {
			rls = append(rls, fmt.Sprintf("%s has row-level security; a row %s can't see fails the undo cleanly.", tc.String(), nonEmpty(in.Catalog.Role, "deedglass_agent")))
		}
	}
	switch {
	case len(missing) > 0:
		add(CheckPrivileges, StatusFail, strings.Join(missing, " "), FixPrivileges, strings.Join(grants, "\n"))
	case len(rls) > 0:
		add(CheckPrivileges, StatusWarn, strings.Join(rls, " "), "", "")
	default:
		add(CheckPrivileges, StatusPass, fmt.Sprintf("%s can %s", nonEmpty(in.Catalog.Role, "deedglass_agent"), strings.Join(can, "; ")), "", "")
	}
	return out
}

// sealedAgo is "sealed just now" under a minute, else "sealed 6 min ago".
func sealedAgo(age time.Duration) string {
	if age < time.Minute {
		return "sealed just now"
	}
	return "sealed " + DurationWords(age) + " ago"
}

func (p *Plan) countOn(relid uint32, op Op) int {
	n := 0
	for _, s := range p.Steps {
		if s.Table == relid && s.Op == op {
			n++
		}
	}
	return n
}

// stepFacts returns the first key the plan deletes in a table (nil when none) and every column it
// writes there with an update.
func stepFacts(p *Plan, relid uint32) (Key, []string) {
	var del Key
	var cols []string
	for _, s := range p.Steps {
		if s.Table != relid {
			continue
		}
		switch s.Op {
		case OpDelete:
			if del == nil {
				del = s.Key
			}
		case OpUpdate:
			for c := range s.Set {
				if !slices.Contains(cols, c) {
					cols = append(cols, c)
				}
			}
		}
	}
	return del, cols
}

func anyIn(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

func fkWord(a string) string {
	switch a {
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	case "r":
		return "RESTRICT"
	}
	return "NO ACTION"
}

func planTables(p *Plan, cat *Catalog) []*TableCatalog {
	var out []*TableCatalog
	for _, t := range p.Tables {
		if tc := cat.Tables[t.RelID]; tc != nil {
			out = append(out, tc)
		}
	}
	return out
}

func joinAnd(ss []string) string {
	switch len(ss) {
	case 0:
		return ""
	case 1:
		return ss[0]
	}
	return strings.Join(ss[:len(ss)-1], ", ") + " and " + ss[len(ss)-1]
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// fixName is "schema.table" for fix SQL, each part quoted only when it needs to be.
func fixName(schema, name string) string { return quoteIdent(schema) + "." + quoteIdent(name) }

// quoteIdent quotes an identifier the way Postgres prints it in fix SQL: bare when it is a plain
// lowercase name, quoted otherwise.
func quoteIdent(s string) string {
	plain := s != ""
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '_' || i > 0 && r >= '0' && r <= '9') {
			plain = false
		}
	}
	if plain {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Mismatch is one guard that doesn't hold.
type Mismatch struct {
	Table string
	Key   string
	Text  string // a person's sentence (undo_conflict's message in RunUndo)
}

// Compare checks every guard against the rows as they are now (S23 check 11): a row the plan
// expects must hold its post-image byte for byte, and a key the plan re-inserts must be absent. It
// returns the guards that don't hold and how many guards there are.
func Compare(p *Plan, cur Current) ([]Mismatch, int) {
	var out []Mismatch
	for _, g := range p.Guards {
		t, _ := p.Table(g.Table)
		kid := g.Key.ID(t.PK)
		row, present := cur[g.Table][kid]
		kt := KeyText(g.Key, t.PK)
		switch {
		case g.Expect == nil && present:
			out = append(out, Mismatch{Table: t.String(), Key: kt, Text: fmt.Sprintf("%s %s exists again since the action.", t.String(), kt)})
		case g.Expect != nil && !present:
			out = append(out, Mismatch{Table: t.String(), Key: kt, Text: fmt.Sprintf("%s %s is gone since the action.", t.String(), kt)})
		case g.Expect != nil:
			if col, now, ok := firstDiff(g.Expect, row); !ok {
				out = append(out, Mismatch{Table: t.String(), Key: kt, Text: fmt.Sprintf("%s %s changed since the action: %s is now %s.", t.String(), kt, col, now)})
			}
		}
	}
	return out, len(p.Guards)
}

// firstDiff compares an expected image with a current row (sorted columns, so the message is
// stable). ok is true when every expected column holds.
func firstDiff(exp map[string]Value, row Image) (col, now string, ok bool) {
	cols := make([]string, 0, len(exp))
	for c := range exp {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	for _, c := range cols {
		e := exp[c]
		got, present := row[c]
		if !present {
			return c, "missing", false
		}
		if !Holds(e, got) {
			if e.D != "" {
				return c, Hidden, false
			}
			return c, ValueText(got), false
		}
	}
	return "", "", true
}

// Holds reports whether a current value (text, NULL or hex as the connector read it) is the
// expected stored value: text byte for byte, a sensitive column by the digest of its text.
func Holds(exp, cur Value) bool {
	switch {
	case exp.N || cur.N:
		return exp.N && cur.N
	case exp.D != "":
		var b []byte
		switch {
		case cur.T != nil:
			b = []byte(*cur.T)
		case cur.X != "":
			x, err := hex.DecodeString(cur.X)
			if err != nil {
				return false
			}
			b = x
		default:
			return false
		}
		sum := sha256.Sum256(b)
		return exp.D == "sha256:"+hex.EncodeToString(sum[:])
	}
	return Same(exp, cur)
}

// Recheck repeats the catalog checks inside the undo's own transaction (S23 §3.3: checks 6 to 12
// run again under locks; 11 is Compare): every plan table still exists under the same name with
// the same primary key, every planned column has the same type and identity, and the replica
// identity, trigger, cascade and privilege checks still pass. It returns the first sentence that
// fails, or "".
func Recheck(p *Plan, cat *Catalog, allowTriggers bool) string {
	if cat == nil {
		return "the catalog couldn't be read"
	}
	for _, t := range p.Tables {
		tc := cat.Tables[t.RelID]
		switch {
		case tc == nil || !tc.Exists:
			return t.String() + " no longer exists"
		case tc.Schema != t.Schema || tc.Name != t.Name:
			return t.String() + " was renamed"
		case !slices.Equal(tc.PK, t.PK):
			return "the primary key of " + t.String() + " changed"
		}
		for _, pc := range t.Columns {
			cc, ok := tc.Column(pc.Name)
			if !ok || cc.Type != pc.Type || (cc.Identity == "a") != pc.IdentityAlways || cc.Generated != "" {
				return t.String() + "." + pc.Name + " changed"
			}
		}
	}
	in := CheckInput{Plan: p, Catalog: cat, AllowTriggers: allowTriggers, Current: Current{}}
	for _, c := range Checks(in) {
		switch c.ID {
		case CheckReplicaIdentity, CheckTriggers, CheckCascades, CheckPrivileges:
			if c.Status == StatusFail {
				return CheckTitles[c.ID] + ": " + c.Detail
			}
		}
	}
	return ""
}
