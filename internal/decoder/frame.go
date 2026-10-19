package decoder

import (
	"time"

	"github.com/jackc/pglogrepl"
)

// LSN is a WAL position.
type LSN = pglogrepl.LSN

// Raw is one row of a peek: its LSN and the pgoutput bytes. The peek's xid column is deliberately
// not exposed (ADR-002 C): classification uses frame position and the transactional flag only.
type Raw struct {
	LSN  LSN
	Data []byte
}

// Class is how a frame relates to a Deedglass action (S20 §3.7, ADR-003 in Deedglass names).
type Class string

// The classes. The decoder never decides a verdict: S21 maps each class to reasons.
const (
	ClassBound             Class = "bound"               // C5: one ledger row and one matching marker
	ClassUnattributed      Class = "unattributed"        // C1: row changes, no ledger row, no marker
	ClassForgedMarker      Class = "forged_marker"       // C2: a marker without a ledger row
	ClassForgedMarkerNonTx Class = "forged_marker_nontx" // C3: a non-transactional deedglass message
	ClassMarkerCount       Class = "marker_count"        // C4: more than one ledger row, or not exactly one marker
	ClassMarkerMismatch    Class = "marker_mismatch"     // C4: one of each, naming different actions (or the xid differs)
	ClassForeignLedger     Class = "foreign_ledger"      // a ledger row from another org
	ClassPublicationGap    Class = "publication_gap"     // a marker without a ledger row while the publication changed
	ClassMissing           Class = "missing"             // reconcile: a ledger row with no frame (no commit LSN)
)

// Classes lists every stored class, in a stable order (counters, status).
var Classes = []Class{ClassBound, ClassUnattributed, ClassForgedMarker, ClassForgedMarkerNonTx, ClassMarkerCount,
	ClassMarkerMismatch, ClassForeignLedger, ClassPublicationGap, ClassMissing, ClassRequestIDReused, ClassVersionTagMismatch}

// Value is one column's value in a stored image. Exactly one field is set, except that Text may
// be an empty string.
type Value struct {
	Text      *string `json:"t,omitempty"` // the column's text output under the pinned settings
	Null      bool    `json:"n,omitempty"` // SQL NULL
	Unchanged bool    `json:"u,omitempty"` // unchanged external TOAST ('u'): the value wasn't sent; it didn't change
	Digest    string  `json:"d,omitempty"` // a sensitive column: "sha256:" of the text; the text is never stored
	Hex       string  `json:"x,omitempty"` // text output that isn't valid UTF-8 (a SQL_ASCII database), as hex
	// Leaves is a keyed column (ADR-108: a FHIR server's resource content): the JSON text kept as
	// keyed digests of its leaves (internal/jsonleaf); the text is never stored.
	Leaves map[string]string `json:"k,omitempty"`
}

// Change is one row change, with its images.
type Change struct {
	Seq     int                `json:"seq"`
	Op      string             `json:"op"` // I, U or D
	Table   string             `json:"table"`
	Key     map[string]*string `json:"key"`               // primary key column → text (nil = NULL); null = unknown
	Old     map[string]Value   `json:"old,omitempty"`     // D, and U with an old image
	New     map[string]Value   `json:"new,omitempty"`     // I and U
	Changed []string           `json:"changed,omitempty"` // U with a full old image: the columns whose value differs
	relID   uint32
}

// TableInfo is what a frame knows about one relation: enough for S23 to plan an undo by RelID.
type TableInfo struct {
	RelID    uint32   `json:"relid"`
	PK       []string `json:"pk"`       // primary key columns; empty when the table has none
	Identity string   `json:"identity"` // replica identity: d, n, f or i
	Columns  []string `json:"columns"`
	TypeOIDs []uint32 `json:"type_oids"`
}

// OpCounts are exact counts per table, including changes past the image cap.
type OpCounts struct {
	Insert   int `json:"insert"`
	Update   int `json:"update"`
	Delete   int `json:"delete"`
	Truncate int `json:"truncate"`
}

// LedgerRow is one decoded deedglass.ledger insert (S17's columns), parsed strictly.
type LedgerRow struct {
	ActionID   string  `json:"action_id"`
	OrgID      string  `json:"org_id"`
	AgentID    string  `json:"agent_id"`
	IntentHash *string `json:"intent_hash"`
	SQLDigest  string  `json:"sql_digest"`
	XID8       string  `json:"xid8"`
	CreatedAt  string  `json:"created_at"`
	Invalid    string  `json:"invalid,omitempty"` // why the row didn't parse; the other fields are then best effort
}

// Frame is one committed transaction (or, for forged_marker_nontx, one message outside any).
type Frame struct {
	Xid        uint32 // BEGIN.xid; informational, never used for grouping outside the frame
	CommitLSN  LSN
	EndLSN     LSN
	CommitTime time.Time
	Class      Class
	ActionID   string // bound, marker_count, marker_mismatch, publication_gap (and missing)
	MarkerLSN  LSN    // evidence only; NEVER compared with CommitLSN (ADR-003 point 5)
	Ledger     []LedgerRow
	Markers    []string // transactional deedglass message contents, in order (an act id, or sha256: of anything else)
	Tables     map[string]TableInfo
	Counts     map[string]OpCounts
	Changes    []Change // the first MaxImages changes only
	Total      int      // every row change (insert, update, delete, truncate) outside the ledger's own inserts
	Digest     string   // sha256 over every change in stream order, including those past the cap
	Flags      []string
	NonTx      bool // a message outside any transaction (forged_marker_nontx)

	other int // transactional messages with other prefixes
}

// NonTxMessage is a non-transactional logical message (outside any frame).
type NonTxMessage struct {
	LSN     LSN
	Prefix  string
	Content []byte
}

// Batch is one peek's assembled output.
type Batch struct {
	Frames []*Frame
	NonTx  []NonTxMessage
	// Relations are the relids (and their shape keys) seen in this batch; the runner looks up the
	// primary key of new ones before it applies keys.
	Relations map[uint32]string
	// LastCommitEnd is the largest COMMIT end LSN; LastNonTx the largest non-transactional LSN.
	LastCommitEnd LSN
	LastNonTx     LSN
	Changes       int // row changes seen (the unit upto_nchanges counts)
}

func (f *Frame) flag(s string) {
	for _, x := range f.Flags {
		if x == s {
			return
		}
	}
	f.Flags = append(f.Flags, s)
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
