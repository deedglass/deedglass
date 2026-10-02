package undo

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/deedglass/deedglass/internal/deed"
)

// PlanVersion is the plan format's version.
const PlanVersion = 1

// Counts are a plan's steps by operation.
type Counts struct {
	Update int `json:"update"`
	Insert int `json:"insert"`
	Delete int `json:"delete"`
}

// Of returns the count for op.
func (c Counts) Of(op Op) int {
	switch op {
	case OpUpdate:
		return c.Update
	case OpInsert:
		return c.Insert
	case OpDelete:
		return c.Delete
	}
	return 0
}

// PlanColumn is one column a plan writes or guards, with its type as the catalog printed it.
type PlanColumn struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	IdentityAlways bool   `json:"identity_always,omitempty"`
}

// PlanTable is one table of a plan: the catalog's identity of it when the plan was made.
type PlanTable struct {
	RelID   uint32       `json:"relid"`
	Schema  string       `json:"schema"`
	Name    string       `json:"name"`
	PK      []string     `json:"pk"`
	Columns []PlanColumn `json:"columns"`
	// Generated are the table's stored generated columns: Postgres recomputes them, so the witness
	// lets them change.
	Generated []string `json:"generated,omitempty"`
}

// String is "schema.name".
func (t PlanTable) String() string { return t.Schema + "." + t.Name }

// Ref is the table's TableRef.
func (t PlanTable) Ref() TableRef { return TableRef{RelID: t.RelID, Schema: t.Schema, Name: t.Name} }

// Type returns a column's type ("" when the plan doesn't name it).
func (t PlanTable) Type(col string) string {
	for _, c := range t.Columns {
		if c.Name == col {
			return c.Type
		}
	}
	return ""
}

// Step is one single-row statement of the undo, in execution order.
type Step struct {
	Op         Op               `json:"op"`
	Table      uint32           `json:"table"` // relid
	Key        Key              `json:"key"`
	Set        map[string]Value `json:"set,omitempty"` // update: the columns put back; insert: every column
	Overriding bool             `json:"overriding,omitempty"`
}

// Guard is what one row must be before any step runs: its post-image from the original
// transaction (Expect), or absent (Expect nil).
type Guard struct {
	Table  uint32           `json:"table"`
	Key    Key              `json:"key"`
	Expect map[string]Value `json:"expect"`
}

// Plan is an undo, built from the evidence and a catalog snapshot (S23 §3.5).
type Plan struct {
	V           int         `json:"v"`
	Undoes      string      `json:"undoes"`
	ConnectorID string      `json:"connector"`
	Evidence    EvidenceRef `json:"evidence"`
	Tables      []PlanTable `json:"tables"`
	Guards      []Guard     `json:"guards"`
	Steps       []Step      `json:"steps"`
	Counts      Counts      `json:"counts"`
	Rows        int         `json:"rows"` // original rows put back
	Digest      string      `json:"digest,omitempty"`
	Notes       []string    `json:"notes,omitempty"`
}

// Table returns the plan table with relid.
func (p *Plan) Table(relid uint32) (PlanTable, bool) {
	for _, t := range p.Tables {
		if t.RelID == relid {
			return t, true
		}
	}
	return PlanTable{}, false
}

// TableNames are the plan's tables, "schema.name", sorted.
func (p *Plan) TableNames() []string {
	out := make([]string, 0, len(p.Tables))
	for _, t := range p.Tables {
		out = append(out, t.String())
	}
	sort.Strings(out)
	return out
}

// ComputeDigest is "sha256:" + hex(sha256(JCS(the plan without its digest and notes))).
func ComputeDigest(p *Plan) (string, error) {
	c := *p
	c.Digest, c.Notes = "", nil
	b, err := json.Marshal(&c)
	if err != nil {
		return "", err
	}
	return deed.DigestJSON(b)
}

// BuildInput is what Build reads.
type BuildInput struct {
	Undoes      string
	ConnectorID string
	Evidence    *Evidence
	Catalog     *Catalog
}

// Build turns the evidence into a plan against the catalog (S23 §3.2). It returns the plan (nil
// when the evidence can't be planned from) and every finding of the checks it decides: evidence,
// primary_key and columns. The plan is a pure function of its input: the same evidence and
// catalog give the same digest.
func Build(in BuildInput) (*Plan, []Finding) {
	ev := in.Evidence
	findings := append([]Finding{}, ev.Problems...)
	fatal := len(ev.Problems) > 0
	pk := map[string][]string{}
	for t, ti := range ev.Tables {
		pk[t] = ti.PK
	}
	net := Collapse(ev.Changes, pk)
	findings = append(findings, net.Problems...)
	if len(net.Problems) > 0 {
		fatal = true
	}
	p := &Plan{V: PlanVersion, Undoes: in.Undoes, ConnectorID: in.ConnectorID, Evidence: ev.Ref, Tables: []PlanTable{},
		Guards: []Guard{}, Steps: []Step{}}
	// The catalog's view of every table the net effect touches.
	tables := map[uint32]*TableCatalog{}
	inserts := map[uint32]bool{}
	for _, n := range net.Rows {
		if n.Before != nil && n.After == nil || n.Kind() == "moved" {
			inserts[n.Table.RelID] = true
		}
	}
	var relids []uint32
	seen := map[uint32]bool{}
	for _, n := range net.Rows {
		if n.Kind() == "none" || seen[n.Table.RelID] {
			continue
		}
		seen[n.Table.RelID] = true
		relids = append(relids, n.Table.RelID)
	}
	slices.Sort(relids)
	for _, relid := range relids {
		ref := refOf(net.Rows, relid)
		tc, fs := tableFindings(ref, ev, in.Catalog, inserts[relid])
		findings = append(findings, fs...)
		if len(fs) > 0 {
			fatal = true
		}
		tables[relid] = tc
	}
	if fatal {
		return nil, sortFindings(findings)
	}
	// Steps, in reverse order of each row's last change: children are deleted before parents and
	// parents re-inserted before children.
	rows := append([]*Net{}, net.Rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeq > rows[j].LastSeq })
	used := map[uint32]map[string]bool{} // relid → columns the plan writes or guards
	use := func(relid uint32, img map[string]Value) {
		if used[relid] == nil {
			used[relid] = map[string]bool{}
		}
		for c := range img {
			used[relid][c] = true
		}
	}
	notes := map[string]bool{}
	var problems []Finding
	for _, n := range rows {
		tc := tables[n.Table.RelID]
		t := n.Table.String()
		switch n.Kind() {
		case "none":
			notes[NoteNoop] = true
			continue
		case "created":
			p.Steps = append(p.Steps, Step{Op: OpDelete, Table: n.Table.RelID, Key: n.AfterKey})
			p.Counts.Delete++
		case "removed":
			set, fs := writable(t, tc, n.Before, nil)
			problems = append(problems, fs...)
			p.Steps = append(p.Steps, Step{Op: OpInsert, Table: n.Table.RelID, Key: n.BeforeKey, Set: set, Overriding: overriding(tc, set)})
			p.Counts.Insert++
			use(n.Table.RelID, set)
		case "changed", "replaced":
			set, fs := writable(t, tc, n.Before, n.After)
			problems = append(problems, fs...)
			if len(set) == 0 {
				notes[NoteSameValue] = true
				continue
			}
			p.Steps = append(p.Steps, Step{Op: OpUpdate, Table: n.Table.RelID, Key: n.AfterKey, Set: set})
			p.Counts.Update++
			use(n.Table.RelID, set)
		case "moved":
			if keyIdentityAlways(tc) {
				// Postgres allows only DEFAULT in an UPDATE of a GENERATED ALWAYS identity column:
				// delete the row at its new key, insert it back at its old one.
				set, fs := writable(t, tc, n.Before, nil)
				problems = append(problems, fs...)
				p.Steps = append(p.Steps, Step{Op: OpDelete, Table: n.Table.RelID, Key: n.AfterKey},
					Step{Op: OpInsert, Table: n.Table.RelID, Key: n.BeforeKey, Set: set, Overriding: overriding(tc, set)})
				p.Counts.Delete++
				p.Counts.Insert++
				use(n.Table.RelID, set)
				break
			}
			set, fs := writable(t, tc, n.Before, n.After)
			problems = append(problems, fs...)
			p.Steps = append(p.Steps, Step{Op: OpUpdate, Table: n.Table.RelID, Key: n.AfterKey, Set: set})
			p.Counts.Update++
			use(n.Table.RelID, set)
		}
		p.Rows++
	}
	// Guards: the original transaction's end state for every key a step names.
	type gk struct {
		relid uint32
		id    string
	}
	guards := map[gk]Guard{}
	for _, n := range net.Rows {
		if n.Kind() == "none" {
			continue
		}
		tc := tables[n.Table.RelID]
		if n.After != nil {
			exp := guardImage(tc, n.After)
			guards[gk{n.Table.RelID, n.AfterKey.ID(tc.PK)}] = Guard{Table: n.Table.RelID, Key: n.AfterKey, Expect: exp}
			use(n.Table.RelID, exp)
		}
	}
	for _, n := range net.Rows {
		if n.Kind() == "none" || n.Before == nil {
			continue
		}
		tc := tables[n.Table.RelID]
		k := gk{n.Table.RelID, n.BeforeKey.ID(tc.PK)}
		if _, ok := guards[k]; !ok {
			guards[k] = Guard{Table: n.Table.RelID, Key: n.BeforeKey}
		}
	}
	for _, g := range guards {
		p.Guards = append(p.Guards, g)
	}
	sort.Slice(p.Guards, func(i, j int) bool {
		a, b := p.Guards[i], p.Guards[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		return a.Key.ID(tables[a.Table].PK) < b.Key.ID(tables[b.Table].PK)
	})
	// The order Postgres accepts one statement at a time.
	gmap := map[uint32]map[string]Guard{}
	for _, g := range p.Guards {
		if gmap[g.Table] == nil {
			gmap[g.Table] = map[string]Guard{}
		}
		gmap[g.Table][g.Key.ID(tables[g.Table].PK)] = g
	}
	ordered, ok := orderSteps(p.Steps, tables, gmap)
	if !ok {
		problems = append(problems, ProblemCycle(p.tableNamesOf(tables)))
	}
	p.Steps = ordered
	// The tables, with the columns the plan writes or guards and their types.
	for _, relid := range relids {
		tc := tables[relid]
		if tc == nil || len(used[relid]) == 0 && !hasStep(p.Steps, relid) {
			continue
		}
		pt := PlanTable{RelID: relid, Schema: tc.Schema, Name: tc.Name, PK: append([]string{}, tc.PK...), Columns: []PlanColumn{}}
		for _, c := range tc.Columns {
			if c.Generated == "s" {
				pt.Generated = append(pt.Generated, c.Name)
			}
			if c.Generated == "" && (used[relid][c.Name] || slices.Contains(tc.PK, c.Name)) {
				pt.Columns = append(pt.Columns, PlanColumn{Name: c.Name, Type: c.Type, IdentityAlways: c.Identity == "a"})
			}
		}
		p.Tables = append(p.Tables, pt)
	}
	sort.Slice(p.Tables, func(i, j int) bool { return p.Tables[i].String() < p.Tables[j].String() })
	if len(problems) > 0 {
		return nil, sortFindings(append(findings, problems...))
	}
	if p.Counts.Insert > 0 {
		notes[NoteSequences] = true
	}
	for _, g := range p.Guards {
		for _, v := range g.Expect {
			if v.D != "" {
				notes[NoteSensitive] = true
			}
		}
	}
	for _, n := range []string{NoteSequences, NoteSensitive, NoteNoop, NoteSameValue} {
		if notes[n] {
			p.Notes = append(p.Notes, n)
		}
	}
	d, err := ComputeDigest(p)
	if err != nil {
		return nil, sortFindings(append(findings, Finding{Check: CheckEvidence, Text: "The plan couldn't be written down: " + err.Error()}))
	}
	p.Digest = d
	return p, sortFindings(findings)
}

// tableNamesOf names the tables the steps touch.
func (p *Plan) tableNamesOf(tables map[uint32]*TableCatalog) []string {
	set := map[string]bool{}
	for _, st := range p.Steps {
		if tc := tables[st.Table]; tc != nil {
			set[tc.String()] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func hasStep(steps []Step, relid uint32) bool {
	for _, s := range steps {
		if s.Table == relid {
			return true
		}
	}
	return false
}

func refOf(rows []*Net, relid uint32) TableRef {
	for _, n := range rows {
		if n.Table.RelID == relid {
			return n.Table
		}
	}
	return TableRef{RelID: relid}
}

// tableFindings checks one table against the catalog: it still exists under the same name, its
// primary key is the evidence's, every evidence column is still there with the same type and
// attributes, a re-insert writes every live column, and every type name is safe in a cast.
func tableFindings(ref TableRef, ev *Evidence, cat *Catalog, inserts bool) (*TableCatalog, []Finding) {
	t := ref.String()
	var fs []Finding
	var tc *TableCatalog
	if cat != nil {
		tc = cat.Tables[ref.RelID]
	}
	if tc == nil || !tc.Exists {
		return nil, []Finding{{Check: CheckColumns, Text: fmt.Sprintf("%s no longer exists.", t)}}
	}
	if tc.Schema != ref.Schema || tc.Name != ref.Name {
		return nil, []Finding{{Check: CheckColumns, Text: fmt.Sprintf("%s is now named %s.%s, so the plan won't guess that it is the same table.", t, tc.Schema, tc.Name)}}
	}
	ti := ev.Tables[t]
	switch {
	case len(tc.PK) == 0:
		fs = append(fs, ProblemNoPrimaryKey(t))
	case !slices.Equal(tc.PK, ti.PK):
		fs = append(fs, Finding{Check: CheckPrimaryKey, Text: fmt.Sprintf("The primary key of %s is now (%s); the action's rows were keyed by (%s).",
			t, joinComma(tc.PK), joinComma(ti.PK))})
	}
	for i, col := range ti.Columns {
		cc, ok := tc.Column(col)
		if !ok {
			fs = append(fs, Finding{Check: CheckColumns, Text: fmt.Sprintf("%s.%s was dropped since the action.", t, col)})
			continue
		}
		if i < len(ti.TypeOIDs) && ti.TypeOIDs[i] != 0 && cc.TypeOID != ti.TypeOIDs[i] {
			fs = append(fs, Finding{Check: CheckColumns, Text: fmt.Sprintf("%s.%s changed type since the action (now %s).", t, col, cc.Type)})
		}
	}
	for _, cc := range tc.Columns {
		if cc.Generated == "" && !ValidTypeName(cc.Type) {
			fs = append(fs, Finding{Check: CheckColumns, Text: fmt.Sprintf("%s.%s has a type name Deedglass won't put in a statement.", t, cc.Name)})
		}
		if inserts && cc.Generated == "" && !slices.Contains(ti.Columns, cc.Name) {
			fs = append(fs, Finding{Check: CheckColumns, Text: fmt.Sprintf("%s.%s was added since the action, so a row put back would get a value Postgres never recorded.", t, cc.Name)})
		}
	}
	return tc, fs
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// writable is what a step writes: with after nil, every non-generated column of before (an
// insert); otherwise the non-generated columns whose value differs between before and after. A
// sensitive value can't be written: only its digest was kept.
func writable(table string, tc *TableCatalog, before, after Image) (map[string]Value, []Finding) {
	set := map[string]Value{}
	var fs []Finding
	cols := make([]string, 0, len(before))
	for c := range before {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	for _, c := range cols {
		cc, ok := tc.Column(c)
		if !ok || cc.Generated != "" {
			continue
		}
		b := before[c]
		if after != nil {
			if a, ok := after[c]; ok && Same(a, b) {
				continue
			}
		}
		if b.U {
			fs = append(fs, ProblemToast(table))
			continue
		}
		if b.D != "" {
			fs = append(fs, ProblemSensitive(table, c))
			continue
		}
		set[c] = b
	}
	if after != nil {
		// A column the new image has but the old one doesn't: the old value is unknown.
		for c := range after {
			cc, ok := tc.Column(c)
			if _, inBefore := before[c]; ok && cc.Generated == "" && !inBefore {
				fs = append(fs, ProblemOldImage(table, ""))
				break
			}
		}
	}
	return set, fs
}

// guardImage is the post-image a guard compares: every non-generated column the image holds.
func guardImage(tc *TableCatalog, img Image) map[string]Value {
	out := map[string]Value{}
	for c, v := range img {
		cc, ok := tc.Column(c)
		if !ok || cc.Generated != "" || v.U {
			continue
		}
		out[c] = v
	}
	return out
}

func overriding(tc *TableCatalog, set map[string]Value) bool {
	for c := range set {
		if cc, ok := tc.Column(c); ok && cc.Identity == "a" {
			return true
		}
	}
	return false
}

func keyIdentityAlways(tc *TableCatalog) bool {
	for _, k := range tc.PK {
		if cc, ok := tc.Column(k); ok && cc.Identity == "a" {
			return true
		}
	}
	return false
}

// orderSteps puts the steps in an order Postgres accepts one statement at a time (S23 §3.2). The
// default is reverse order of each row's last change in the original transaction (children deleted
// before parents, parents put back before children), and these rules win over it:
//   - a key is freed (a delete there, or a row moving away) before a row takes it (an insert there,
//     or a row moving onto it);
//   - a row that references a parent key (a foreign key on the parent's primary key) is put back
//     after the step that puts that parent row back, and stops referencing a parent row before
//     the step that removes it. This covers ON DELETE CASCADE, which Postgres logs parent first.
//
// A cycle (rows that swapped keys) can't be put back one row at a time: ok is false.
func orderSteps(steps []Step, tables map[uint32]*TableCatalog, guards map[uint32]map[string]Guard) ([]Step, bool) {
	n := len(steps)
	type info struct {
		cur, next       map[string]Value // the row before and after the step (nil: absent)
		curKey, nextKey string           // canonical keys, "" when absent
	}
	inf := make([]info, n)
	type slot struct {
		relid uint32
		key   string
	}
	occupy, vacate := map[slot][]int{}, map[slot][]int{}
	for i, s := range steps {
		tc := tables[s.Table]
		g := guards[s.Table][s.Key.ID(tc.PK)]
		switch s.Op {
		case OpInsert:
			inf[i].next, inf[i].nextKey = s.Set, s.Key.ID(tc.PK)
			occupy[slot{s.Table, inf[i].nextKey}] = append(occupy[slot{s.Table, inf[i].nextKey}], i)
		case OpDelete:
			inf[i].cur, inf[i].curKey = g.Expect, s.Key.ID(tc.PK)
			vacate[slot{s.Table, inf[i].curKey}] = append(vacate[slot{s.Table, inf[i].curKey}], i)
		case OpUpdate:
			next := map[string]Value{}
			for c, v := range g.Expect {
				next[c] = v
			}
			for c, v := range s.Set {
				next[c] = v
			}
			nk := Key{}
			for _, k := range tc.PK {
				nk[k] = s.Key[k]
				if v, ok := s.Set[k]; ok && v.T != nil {
					nk[k] = *v.T
				}
			}
			inf[i].cur, inf[i].next, inf[i].curKey, inf[i].nextKey = g.Expect, next, s.Key.ID(tc.PK), nk.ID(tc.PK)
			if inf[i].curKey != inf[i].nextKey {
				vacate[slot{s.Table, inf[i].curKey}] = append(vacate[slot{s.Table, inf[i].curKey}], i)
				occupy[slot{s.Table, inf[i].nextKey}] = append(occupy[slot{s.Table, inf[i].nextKey}], i)
			}
		}
	}
	after := make([][]int, n) // after[i]: the steps that must wait for i
	indeg := make([]int, n)
	edge := func(first, then int) {
		if first == then {
			return
		}
		after[first] = append(after[first], then)
		indeg[then]++
	}
	for k, occ := range occupy {
		for _, j := range occ {
			for _, i := range vacate[k] {
				edge(i, j)
			}
		}
	}
	// Foreign keys on a parent's primary key, between tables of the plan.
	byName := map[string]uint32{}
	for relid, tc := range tables {
		byName[tc.String()] = relid
	}
	refKey := func(img map[string]Value, from, to []string) (Key, bool) {
		if img == nil {
			return nil, false
		}
		k := Key{}
		for i, c := range from {
			v, ok := img[c]
			if !ok || v.T == nil || v.N {
				return nil, false
			}
			k[to[i]] = *v.T
		}
		return k, true
	}
	for prel, ptc := range tables {
		for _, fk := range ptc.Referrers {
			crel, ok := byName[fk.From]
			if !ok || len(fk.FromColumns) != len(fk.Columns) || !sameSet(fk.Columns, ptc.PK) {
				continue
			}
			for x, s := range steps {
				if s.Table != crel {
					continue
				}
				nk, nok := refKey(inf[x].next, fk.FromColumns, fk.Columns)
				ck, cok := refKey(inf[x].cur, fk.FromColumns, fk.Columns)
				if nok {
					// The parent row this one will reference must be there first.
					for _, y := range occupy[slot{prel, nk.ID(ptc.PK)}] {
						edge(y, x)
					}
				}
				if cok && !(nok && ck.ID(ptc.PK) == nk.ID(ptc.PK)) {
					// Stop referencing a parent row before it is removed or moved away.
					for _, y := range vacate[slot{prel, ck.ID(ptc.PK)}] {
						edge(x, y)
					}
				}
			}
		}
	}
	// Kahn's algorithm, always taking the earliest step in the default order that is free.
	out := make([]Step, 0, n)
	done := make([]bool, n)
	for len(out) < n {
		next := -1
		for i := 0; i < n; i++ {
			if !done[i] && indeg[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			return steps, false
		}
		done[next] = true
		out = append(out, steps[next])
		for _, j := range after[next] {
			indeg[j]--
		}
	}
	return out, true
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}
