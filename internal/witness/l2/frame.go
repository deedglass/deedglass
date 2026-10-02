package l2

import (
	"encoding/json"
	"fmt"
	"time"
)

// Frame classes that name an action (S20 §3.7). The decoder never decides a verdict; Evaluate maps
// each class to reasons.
const (
	ClassBound          = "bound"
	ClassMarkerCount    = "marker_count"
	ClassMarkerMismatch = "marker_mismatch"
	ClassPublicationGap = "publication_gap"
	ClassMissing        = "missing"
)

// LedgerTable is deedglass.ledger; it is never part of a footprint (S21 §3.3).
const LedgerTable = "deedglass.ledger"

// Value is one column's value in a stored image (decoder.Value's JSON): exactly one field is set,
// except that T may be an empty string.
type Value struct {
	T *string `json:"t,omitempty"` // Postgres's text output under the decoder's pinned settings
	N bool    `json:"n,omitempty"` // SQL NULL
	U bool    `json:"u,omitempty"` // an unchanged TOAST value Postgres didn't send
	D string  `json:"d,omitempty"` // a sensitive column: only "sha256:" of the text was stored
	X string  `json:"x,omitempty"` // text output that isn't UTF-8, as hex
}

// Text is the value as a person reads it: the text, NULL, "•••" for a sensitive column. ok is false
// when the image doesn't hold the value (unchanged TOAST).
func (v Value) Text() (s string, ok bool) {
	switch {
	case v.D != "":
		return Hidden, true
	case v.N:
		return "NULL", true
	case v.U:
		return "", false
	case v.X != "":
		return `\x` + v.X, true
	case v.T != nil:
		return *v.T, true
	}
	return "", false
}

// Hidden is how a sensitive value is shown everywhere (brand §12; S20 stored only its digest).
const Hidden = "•••"

// Change is one row change with its images (decoder.Change's JSON).
type Change struct {
	Seq     int                `json:"seq"`
	Op      string             `json:"op"` // I, U or D
	Table   string             `json:"table"`
	Key     map[string]*string `json:"key"` // primary key column → text (nil = NULL); nil map = unknown
	Old     map[string]Value   `json:"old,omitempty"`
	New     map[string]Value   `json:"new,omitempty"`
	Changed []string           `json:"changed,omitempty"` // U with a full old image: the columns whose value differs
}

// TableInfo is what a frame knows about one relation.
type TableInfo struct {
	RelID    uint32   `json:"relid"`
	PK       []string `json:"pk"`
	Identity string   `json:"identity"` // replica identity: d, n, f or i
	Columns  []string `json:"columns"`
	TypeOIDs []uint32 `json:"type_oids"`
}

// TypeOf returns a column's type OID, or 0.
func (t TableInfo) TypeOf(col string) uint32 {
	for i, c := range t.Columns {
		if c == col && i < len(t.TypeOIDs) {
			return t.TypeOIDs[i]
		}
	}
	return 0
}

// OpCounts are exact per-table counts, past the image cap too.
type OpCounts struct {
	Insert   int `json:"insert"`
	Update   int `json:"update"`
	Delete   int `json:"delete"`
	Truncate int `json:"truncate"`
}

// Rows is every row-level change (truncates are not rows).
func (c OpCounts) Rows() int { return c.Insert + c.Update + c.Delete }

// Of returns the count of one operation ("insert", "update", "delete", "truncate").
func (c OpCounts) Of(op string) int {
	switch op {
	case OpInsert:
		return c.Insert
	case OpUpdate:
		return c.Update
	case OpDelete:
		return c.Delete
	case OpTruncate:
		return c.Truncate
	}
	return 0
}

// LedgerRow is one decoded deedglass.ledger insert.
type LedgerRow struct {
	ActionID   string  `json:"action_id"`
	OrgID      string  `json:"org_id"`
	AgentID    string  `json:"agent_id"`
	IntentHash *string `json:"intent_hash"`
	SQLDigest  string  `json:"sql_digest"`
	XID8       string  `json:"xid8"`
	CreatedAt  string  `json:"created_at"`
	Invalid    string  `json:"invalid,omitempty"`
}

// Frame is one committed transaction that names an action, as plain data (a decoded_txs row).
type Frame struct {
	ID         string
	Class      string
	SystemID   string
	Timeline   int
	CommitLSN  string
	Xid        int64
	CommitTime time.Time
	MarkerLSN  string // evidence only; never compared with CommitLSN (ADR-003 point 5)
	Ledger     []LedgerRow
	Markers    []string
	Tables     map[string]TableInfo
	Counts     map[string]OpCounts
	Changes    []Change // the first decoder.max_images_per_tx changes only
	Total      int      // every change, past the cap too (the ledger's own insert excluded)
	Digest     string
	Flags      []string
}

// HasFlag reports whether the frame carries flag s.
func (f *Frame) HasFlag(s string) bool {
	for _, x := range f.Flags {
		if x == s {
			return true
		}
	}
	return false
}

// imaged reports whether every change on t has an image (no images_truncated, or t's changes all
// fit before the cap).
func (f *Frame) imaged(t string) bool {
	if !f.HasFlag("images_truncated") {
		return true
	}
	n := 0
	for _, c := range f.Changes {
		if c.Table == t {
			n++
		}
	}
	return n >= f.Counts[t].Rows()
}

// ParseParts reads a stored frame's JSON columns (ledger, tables, counts, changes) into f.
func (f *Frame) ParseParts(ledger, tables, counts, changes []byte) error {
	read := func(name string, b []byte, v any) error {
		if len(b) == 0 || string(b) == "null" {
			return nil
		}
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("l2: the frame's %s don't parse: %w", name, err)
		}
		return nil
	}
	if err := read("ledger rows", ledger, &f.Ledger); err != nil {
		return err
	}
	if err := read("tables", tables, &f.Tables); err != nil {
		return err
	}
	if err := read("counts", counts, &f.Counts); err != nil {
		return err
	}
	return read("changes", changes, &f.Changes)
}

// Operations, as footprints and diffs name them.
const (
	OpInsert   = "insert"
	OpUpdate   = "update"
	OpDelete   = "delete"
	OpTruncate = "truncate"
)

// opName maps a change's I/U/D to its operation.
func opName(op string) string {
	switch op {
	case "I":
		return OpInsert
	case "U":
		return OpUpdate
	case "D":
		return OpDelete
	}
	return op
}
