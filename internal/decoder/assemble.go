package decoder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pglogrepl"

	"github.com/deedglass/deedglass/internal/jsonleaf"
)

// DefaultMaxImages is decoder.max_images_per_tx: S23's undo row limit.
const DefaultMaxImages = 1000

// AssembleOptions configure an Assembler.
type AssembleOptions struct {
	// Sensitive columns ("schema.table.column", the connector's witness.sensitive_columns) are kept
	// only as a digest of their text.
	Sensitive map[string]bool
	// MaxImages is how many changes per frame keep their images (counts and the digest cover all).
	MaxImages int
	// Keyed columns (ADR-108: a FHIR server's resource content) hold JSON documents that are kept
	// only as keyed digests of their leaves (internal/jsonleaf, with Key); a value that isn't a JSON
	// object or array becomes a digest. An empty text stays as it is.
	Keyed func(table, column string) bool
	Key   []byte
	// CountOnly tables (ADR-108: a FHIR server's search-index and lookup tables) are counted and
	// digested like every change, but their images are never kept.
	CountOnly func(table string) bool
	// Shapes is the persistent relation shape map (relid → shape key), kept by the runner across
	// peeks: a relation that arrives with a new shape flags relation_changed:<table>, the only
	// trace DDL leaves in the stream. Updated in place; nil means no memory.
	Shapes map[uint32]string
}

// Assembler turns one peek's parsed messages into frames (S20 §3.6). It is pure: no I/O, and it
// holds at most MaxImages images per frame. The relation cache is rebuilt from each peek's R
// messages (ADR-002 F: R is re-sent per SQL call), so a new Assembler is used per peek.
type Assembler struct {
	o       AssembleOptions
	rels    map[uint32]*pglogrepl.RelationMessage
	cur     *Frame
	dig     *digester
	seq     int
	pending []string // relation_changed flags from an R outside a frame
	b       Batch
}

// NewAssembler returns an assembler for one peek.
func NewAssembler(o AssembleOptions) *Assembler {
	if o.MaxImages <= 0 {
		o.MaxImages = DefaultMaxImages
	}
	return &Assembler{o: o, rels: map[uint32]*pglogrepl.RelationMessage{}, b: Batch{Relations: map[uint32]string{}}}
}

func malformed(t byte, reason, detail string) *MalformedError {
	return &MalformedError{Type: t, Reason: reason, Detail: detail}
}

// Add feeds one message (already through SafeParse) at peek position lsn.
func (a *Assembler) Add(lsn LSN, m pglogrepl.Message) error {
	switch m := m.(type) {
	case *pglogrepl.BeginMessage:
		if a.cur != nil {
			return malformed('B', "nested_begin", "")
		}
		a.cur = &Frame{Xid: m.Xid, Tables: map[string]TableInfo{}, Counts: map[string]OpCounts{}}
		a.dig, a.seq = newDigester(), 0
		for _, f := range a.pending {
			a.cur.flag(f)
		}
		a.pending = nil
	case *pglogrepl.CommitMessage:
		f := a.cur
		if f == nil {
			return malformed('C', "commit_without_begin", "")
		}
		f.CommitLSN, f.EndLSN, f.CommitTime = m.CommitLSN, m.TransactionEndLSN, m.CommitTime.UTC()
		f.Digest = a.dig.sum()
		if f.other > 0 {
			f.flag("other_messages:" + strconv.Itoa(f.other))
		}
		a.b.Frames = append(a.b.Frames, f)
		if m.TransactionEndLSN > a.b.LastCommitEnd {
			a.b.LastCommitEnd = m.TransactionEndLSN
		}
		a.cur = nil
	case *pglogrepl.RelationMessage:
		key := shapeKey(m)
		if a.o.Shapes != nil {
			if prev, ok := a.o.Shapes[m.RelationID]; ok && prev != key {
				flag := "relation_changed:" + relName(m)
				if a.cur != nil {
					a.cur.flag(flag)
				} else {
					a.pending = append(a.pending, flag)
				}
			}
			a.o.Shapes[m.RelationID] = key
		}
		a.rels[m.RelationID] = m
		a.b.Relations[m.RelationID] = key
	case *pglogrepl.TypeMessage:
		// Type names for non-built-in types; values arrive as text, so nothing to keep.
	case *pglogrepl.OriginMessage:
		if a.cur != nil {
			name := m.Name
			if !printable(name, 64) {
				sum := sha256.Sum256([]byte(name))
				name = "sha256:" + hex.EncodeToString(sum[:8])
			}
			a.cur.flag("origin:" + name)
		}
	case *pglogrepl.InsertMessage:
		return a.change('I', m.RelationID, 0, nil, m.Tuple)
	case *pglogrepl.UpdateMessage:
		return a.change('U', m.RelationID, m.OldTupleType, m.OldTuple, m.NewTuple)
	case *pglogrepl.DeleteMessage:
		return a.change('D', m.RelationID, m.OldTupleType, m.OldTuple, nil)
	case *pglogrepl.TruncateMessage:
		f := a.cur
		if f == nil {
			return malformed('T', "change_outside_transaction", "")
		}
		for _, id := range m.RelationIDs {
			rel := a.rels[id]
			if rel == nil {
				return malformed('T', "unknown_relation", fmt.Sprintf("relid %d", id))
			}
			t := relName(rel)
			a.b.Changes++
			a.seq++
			f.Total++
			c := f.Counts[t]
			c.Truncate++
			f.Counts[t] = c
			a.table(f, t, rel)
			f.flag("truncate:" + t)
			if t == ledgerName {
				f.flag("ledger_modified")
			}
			a.dig.add(a.seq, "T", t, nil, nil)
		}
	case *pglogrepl.LogicalDecodingMessage:
		if !m.Transactional {
			nt := NonTxMessage{LSN: m.LSN, Prefix: m.Prefix, Content: m.Content}
			a.b.NonTx = append(a.b.NonTx, nt)
			if m.LSN > a.b.LastNonTx {
				a.b.LastNonTx = m.LSN
			}
			if lsn > a.b.LastNonTx {
				a.b.LastNonTx = lsn
			}
			return nil
		}
		f := a.cur
		if f == nil {
			return malformed('M', "transactional_message_outside_transaction", "")
		}
		if m.Prefix != MarkerPrefix {
			f.other++
			return nil
		}
		if len(f.Markers) == 0 {
			f.MarkerLSN = m.LSN
		}
		f.Markers = append(f.Markers, markerEvidence(m.Content))
	default:
		return malformed(byte(m.Type()), "unexpected_message_type", fmt.Sprintf("%T", m))
	}
	return nil
}

// Finish returns the batch. A transaction still open is malformed: a peek returns whole
// transactions only.
func (a *Assembler) Finish() (*Batch, error) {
	if a.cur != nil {
		return nil, malformed('B', "incomplete_transaction", "")
	}
	b := a.b
	return &b, nil
}

func relName(r *pglogrepl.RelationMessage) string { return r.Namespace + "." + r.RelationName }

// shapeKey identifies a relation's shape: names, replica identity and every column's name, type,
// typmod and key flag.
func shapeKey(r *pglogrepl.RelationMessage) string {
	var b strings.Builder
	b.WriteString(r.Namespace)
	b.WriteByte(0)
	b.WriteString(r.RelationName)
	b.WriteByte(0)
	b.WriteByte(r.ReplicaIdentity)
	for _, c := range r.Columns {
		fmt.Fprintf(&b, "\x00%s\x00%d\x00%d\x00%d", c.Name, c.DataType, c.TypeModifier, c.Flags)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:12])
}

// table records rel's info in the frame on first use.
func (a *Assembler) table(f *Frame, t string, rel *pglogrepl.RelationMessage) {
	if _, ok := f.Tables[t]; ok {
		return
	}
	ti := TableInfo{RelID: rel.RelationID, Identity: string(rune(rel.ReplicaIdentity)), PK: []string{},
		Columns: make([]string, len(rel.Columns)), TypeOIDs: make([]uint32, len(rel.Columns))}
	for i, c := range rel.Columns {
		ti.Columns[i], ti.TypeOIDs[i] = c.Name, c.DataType
	}
	f.Tables[t] = ti
}

func (a *Assembler) change(op byte, relID uint32, oldType uint8, old, nw *pglogrepl.TupleData) error {
	f := a.cur
	if f == nil {
		return malformed(op, "change_outside_transaction", "")
	}
	rel := a.rels[relID]
	if rel == nil {
		return malformed(op, "unknown_relation", fmt.Sprintf("relid %d", relID))
	}
	for _, td := range []*pglogrepl.TupleData{old, nw} {
		if td != nil && (int(td.ColumnNum) != len(rel.Columns) || len(td.Columns) != len(rel.Columns)) {
			return malformed(op, "tuple_columns", fmt.Sprintf("%s: %d columns in the tuple, %d in the relation", relName(rel), td.ColumnNum, len(rel.Columns)))
		}
	}
	t := relName(rel)
	a.b.Changes++
	if t == ledgerName && op == 'I' {
		f.Ledger = append(f.Ledger, ParseLedgerRow(a.image(rel, t, nw, false)))
		return nil
	}
	if t == ledgerName {
		f.flag("ledger_modified")
	}
	a.seq++
	f.Total++
	c := f.Counts[t]
	var oldImg, newImg map[string]Value
	var changed []string
	switch op {
	case 'I':
		c.Insert++
		newImg = a.image(rel, t, nw, false)
	case 'U':
		c.Update++
		newImg = a.image(rel, t, nw, false)
		switch oldType {
		case 'O':
			oldImg = a.image(rel, t, old, false)
			changed = changedColumns(rel, oldImg, newImg)
		case 'K':
			oldImg = a.image(rel, t, old, true)
			f.flag("old_image_missing:" + t)
		default:
			f.flag("old_image_missing:" + t)
		}
	case 'D':
		c.Delete++
		if oldType == 'O' {
			oldImg = a.image(rel, t, old, false)
		} else {
			oldImg = a.image(rel, t, old, true)
			f.flag("old_image_missing:" + t)
		}
	}
	f.Counts[t] = c
	a.table(f, t, rel)
	opS := string(rune(op))
	a.dig.add(a.seq, opS, t, oldImg, newImg)
	if a.o.CountOnly != nil && a.o.CountOnly(t) {
		return nil
	}
	if len(f.Changes) < a.o.MaxImages {
		f.Changes = append(f.Changes, Change{Seq: a.seq, Op: opS, Table: t, Old: oldImg, New: newImg, Changed: changed, relID: relID})
	} else {
		f.flag("images_truncated")
	}
	return nil
}

// image converts a tuple to column → Value. keyOnly keeps only the relation's key columns (a 'K'
// tuple carries NULLs for the rest, which mustn't read as values).
func (a *Assembler) image(rel *pglogrepl.RelationMessage, t string, td *pglogrepl.TupleData, keyOnly bool) map[string]Value {
	if td == nil {
		return nil
	}
	img := make(map[string]Value, len(td.Columns))
	for i, col := range td.Columns {
		rc := rel.Columns[i]
		if keyOnly && rc.Flags&1 == 0 {
			continue
		}
		var v Value
		switch col.DataType {
		case 'n':
			v.Null = true
		case 'u':
			v.Unchanged = true
		default: // 't' (SafeParse refused everything else)
			switch {
			case a.o.Keyed != nil && len(col.Data) > 0 && a.o.Keyed(t, rc.Name):
				if l, err := jsonleaf.Parse(a.o.Key, col.Data); err == nil && len(col.Data) > 0 && (col.Data[0] == '{' || col.Data[0] == '[') {
					v.Leaves = l
				} else {
					sum := sha256.Sum256(col.Data)
					v.Digest = "sha256:" + hex.EncodeToString(sum[:])
				}
			case a.o.Sensitive[t+"."+rc.Name]:
				sum := sha256.Sum256(col.Data)
				v.Digest = "sha256:" + hex.EncodeToString(sum[:])
			case utf8.Valid(col.Data):
				s := string(col.Data)
				v.Text = &s
			default:
				v.Hex = hex.EncodeToString(col.Data)
			}
		}
		img[rc.Name] = v
	}
	return img
}

// changedColumns lists the columns whose value differs between a full old image and the new one,
// in the relation's order. An unchanged TOAST value ('u') counts as unchanged.
func changedColumns(rel *pglogrepl.RelationMessage, old, nw map[string]Value) []string {
	out := []string{}
	for _, c := range rel.Columns {
		o, n := old[c.Name], nw[c.Name]
		if n.Unchanged || o.Unchanged {
			continue
		}
		if !sameValue(o, n) {
			out = append(out, c.Name)
		}
	}
	return out
}

func sameValue(a, b Value) bool {
	if a.Null != b.Null || a.Digest != b.Digest || a.Hex != b.Hex || (a.Text == nil) != (b.Text == nil) || !maps.Equal(a.Leaves, b.Leaves) {
		return false
	}
	return a.Text == nil || *a.Text == *b.Text
}

// ApplyKeys fills each stored change's primary key and each table's PK from pk (relid → primary
// key columns, from pg_index). A table without one gets no_primary_key:<table>, and its changes a
// null key. The key of an update or delete comes from the old image when it holds the key columns.
func ApplyKeys(fs []*Frame, pk map[uint32][]string) {
	for _, f := range fs {
		for t, ti := range f.Tables {
			cols := pk[ti.RelID]
			if len(cols) == 0 {
				f.flag("no_primary_key:" + t)
				continue
			}
			ti.PK = append([]string{}, cols...)
			f.Tables[t] = ti
		}
		for i := range f.Changes {
			c := &f.Changes[i]
			cols := pk[c.relID]
			if len(cols) == 0 {
				continue
			}
			src := c.New
			if c.Op == "D" || (c.Op == "U" && hasAll(c.Old, cols)) {
				src = c.Old
			}
			if !hasAll(src, cols) {
				continue
			}
			key := make(map[string]*string, len(cols))
			for _, k := range cols {
				v := src[k]
				switch {
				case v.Text != nil:
					s := *v.Text
					key[k] = &s
				case v.Digest != "":
					s := v.Digest
					key[k] = &s
				case v.Hex != "":
					s := "x:" + v.Hex
					key[k] = &s
				default:
					key[k] = nil
				}
			}
			c.Key = key
		}
	}
}

func hasAll(img map[string]Value, cols []string) bool {
	if img == nil {
		return false
	}
	for _, k := range cols {
		v, ok := img[k]
		if !ok || v.Unchanged {
			return false
		}
	}
	return true
}
