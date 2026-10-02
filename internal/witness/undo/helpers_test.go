package undo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/deedglass/deedglass/internal/witness/l2"
)

// Test fixtures: an orders table (relid 100) with a stored generated column, and its order_items
// child (relid 200), as the decoder records them; builders for images, row changes, evidence and
// a catalog snapshot.

const (
	oidInt8 = 20
	oidText = 25
	actID   = "act_01m3x71p6wesj7xewec6bwbwxm"
	connID  = "con_01m3x70000000000000000000c"
)

var (
	ordersRef = TableRef{RelID: 100, Schema: "public", Name: "orders"}
	itemsRef  = TableRef{RelID: 200, Schema: "public", Name: "order_items"}
)

func sp(s string) *string { return &s }

// tv is a text value; nv is NULL; uv is an unchanged TOAST value; dv is a sensitive column's
// digest of text; xv is non-UTF-8 text as hex.
func tv(s string) Value { return Value{T: sp(s)} }

var (
	nv = Value{N: true}
	uv = Value{U: true}
)

func dv(text string) Value { return Value{D: digestOf(text)} }

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func xv(b []byte) Value { return Value{X: hex.EncodeToString(b)} }

// im builds an image from pairs: a column name, then a string (text) or a Value.
func im(pairs ...any) Image {
	out := Image{}
	for i := 0; i+1 < len(pairs); i += 2 {
		col := pairs[i].(string)
		switch v := pairs[i+1].(type) {
		case string:
			out[col] = tv(v)
		case Value:
			out[col] = v
		default:
			panic(fmt.Sprintf("im: %T", v))
		}
	}
	return out
}

// order is one orders row: id, status, a note, and the generated total_cents.
func order(id, status string) Image {
	return im("id", id, "status", status, "note", "n-"+id, "total_cents", strconv.Itoa(len(status)*100))
}

// item is one order_items row.
func item(id, orderID string) Image {
	return im("id", id, "order_id", orderID, "sku", "sku-"+id)
}

// with returns a copy of img with pairs changed.
func with(img Image, pairs ...any) Image {
	out := Image{}
	for k, v := range img {
		out[k] = v
	}
	for k, v := range im(pairs...) {
		out[k] = v
	}
	return out
}

// without returns a copy of img without cols.
func without(img Image, cols ...string) Image {
	out := Image{}
	for k, v := range img {
		out[k] = v
	}
	for _, c := range cols {
		delete(out, c)
	}
	return out
}

func ins(seq int, t TableRef, n Image) RowChange {
	return RowChange{Seq: seq, Op: OpInsert, Table: t, New: n}
}

func upd(seq int, t TableRef, o, n Image) RowChange {
	return RowChange{Seq: seq, Op: OpUpdate, Table: t, Old: o, New: n}
}

func del(seq int, t TableRef, o Image) RowChange {
	return RowChange{Seq: seq, Op: OpDelete, Table: t, Old: o}
}

func ordersInfo() l2.TableInfo {
	return l2.TableInfo{RelID: 100, PK: []string{"id"}, Identity: "f", Columns: []string{"id", "status", "note", "total_cents"},
		TypeOIDs: []uint32{oidInt8, oidText, oidText, oidInt8}}
}

func itemsInfo() l2.TableInfo {
	return l2.TableInfo{RelID: 200, PK: []string{"id"}, Identity: "f", Columns: []string{"id", "order_id", "sku"},
		TypeOIDs: []uint32{oidInt8, oidInt8, oidText}}
}

// evidence wraps changes in the normalized evidence of one bound frame, with both tables described.
func evidence(changes ...RowChange) *Evidence {
	return &Evidence{
		Action: actID, ConnectorID: connID, Class: l2.ClassBound,
		Ref: EvidenceRef{DecodedTx: "dtx_01m3x71p6wesj7xewec6bwbwy0", Xid: 13130, CommitLSN: "0/3E65020",
			CommittedAt: "2026-10-07T13:12:09.114Z", Changes: len(changes), ChangesDigest: "sha256:00"},
		Tables:  map[string]l2.TableInfo{"public.orders": ordersInfo(), "public.order_items": itemsInfo()},
		Changes: changes,
		Total:   len(changes),
	}
}

// ordersCat is the catalog's orders table now: unchanged since the action, every privilege, no
// triggers. identity is attidentity of id ("", "a" or "d").
func ordersCat(identity string) *TableCatalog {
	return &TableCatalog{RelID: 100, Exists: true, Schema: "public", Name: "orders", Kind: "r",
		Columns: []ColumnCatalog{
			{Name: "id", Type: "bigint", TypeOID: oidInt8, Identity: identity},
			{Name: "status", Type: "text", TypeOID: oidText},
			{Name: "note", Type: "text", TypeOID: oidText},
			{Name: "total_cents", Type: "bigint", TypeOID: oidInt8, Generated: "s"},
		},
		PK: []string{"id"}, ReplIdent: "f", Select: true, Insert: true, Update: true, Delete: true}
}

func itemsCat() *TableCatalog {
	return &TableCatalog{RelID: 200, Exists: true, Schema: "public", Name: "order_items", Kind: "r",
		Columns: []ColumnCatalog{
			{Name: "id", Type: "bigint", TypeOID: oidInt8},
			{Name: "order_id", Type: "bigint", TypeOID: oidInt8},
			{Name: "sku", Type: "text", TypeOID: oidText},
		},
		PK: []string{"id"}, ReplIdent: "f", Select: true, Insert: true, Update: true, Delete: true}
}

func catalog(tcs ...*TableCatalog) *Catalog {
	c := &Catalog{Role: "deedglass_agent", Tables: map[uint32]*TableCatalog{}}
	for _, tc := range tcs {
		c.Tables[tc.RelID] = tc
	}
	return c
}

// defaultCatalog has both tables as they were, and order_items.order_id referencing orders.id
// (no action: it only makes a wrong order fail).
func defaultCatalog() *Catalog {
	o := ordersCat("")
	o.Referrers = []ForeignKey{{Name: "order_items_order_id_fkey", From: "public.order_items", FromColumns: []string{"order_id"},
		Columns: []string{"id"}, OnDelete: "a", OnUpdate: "a"}}
	return catalog(o, itemsCat())
}

func build(ev *Evidence, cat *Catalog) (*Plan, []Finding) {
	return Build(BuildInput{Undoes: actID, ConnectorID: connID, Evidence: ev, Catalog: cat})
}

// key is a single-column key.
func key(id string) Key { return Key{"id": id} }

// stepsText writes steps compactly: "delete public.orders id=7".
func stepsText(p *Plan) []string {
	var out []string
	for _, s := range p.Steps {
		t, _ := p.Table(s.Table)
		line := fmt.Sprintf("%s %s %s", s.Op, t.String(), KeyText(s.Key, t.PK))
		if s.Overriding {
			line += " overriding"
		}
		out = append(out, line)
	}
	return out
}

// hasFinding reports whether fs holds a finding of check with text.
func hasFinding(fs []Finding, check, text string) bool {
	for _, f := range fs {
		if f.Check == check && f.Text == text {
			return true
		}
	}
	return false
}

var (
	t0 = time.Date(2026, 10, 7, 13, 12, 9, 114e6, time.UTC)
)
