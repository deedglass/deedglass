package decoder

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/deedglass/deedglass/internal/ids"
)

// LedgerSchema and LedgerTable name S17's ledger in the customer database (contract §4).
const (
	LedgerSchema = "deedglass"
	LedgerTable  = "ledger"
	ledgerName   = LedgerSchema + "." + LedgerTable
)

// ledgerColumns are S17's columns, exactly (setup/sql/20_schema.sql).
var ledgerColumns = []string{"action_id", "org_id", "agent_id", "intent_hash", "sql_digest", "xid8", "created_at"}

var (
	reDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// timestamptz under the pinned TimeZone=UTC and DateStyle=ISO, YMD.
	reLedgerTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?\+00$`)
	reXID8       = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// ParseLedgerRow parses one decoded ledger insert strictly: the seven columns and nothing else,
// ids in contract §3's shapes, digests as sha256:<hex>, xid8 as a decimal, created_at in the
// pinned ISO form. A row that fails is returned with Invalid set (the frame is never bound).
func ParseLedgerRow(img map[string]Value) LedgerRow {
	var r LedgerRow
	text := func(col string) (string, bool) {
		v, ok := img[col]
		if !ok || v.Text == nil {
			return "", false
		}
		return *v.Text, true
	}
	bad := func(why string) LedgerRow {
		if r.Invalid == "" {
			r.Invalid = why
		}
		return r
	}
	if len(img) != len(ledgerColumns) {
		bad(fmt.Sprintf("ledger_shape: %d columns, want %d", len(img), len(ledgerColumns)))
	}
	for _, c := range ledgerColumns {
		if _, ok := img[c]; !ok {
			bad("ledger_shape: no column " + c)
		}
	}
	var ok bool
	if r.ActionID, ok = text("action_id"); !ok || !validID[ids.ActionKind](r.ActionID) {
		bad("action_id")
	}
	if r.OrgID, ok = text("org_id"); !ok || !validID[ids.OrgKind](r.OrgID) {
		bad("org_id")
	}
	if r.AgentID, ok = text("agent_id"); !ok || !validID[ids.AgentKind](r.AgentID) {
		bad("agent_id")
	}
	if v, present := img["intent_hash"]; present && !v.Null {
		if v.Text == nil || !reDigest.MatchString(*v.Text) {
			bad("intent_hash")
		} else {
			h := *v.Text
			r.IntentHash = &h
		}
	}
	if r.SQLDigest, ok = text("sql_digest"); !ok || !reDigest.MatchString(r.SQLDigest) {
		bad("sql_digest")
	}
	if r.XID8, ok = text("xid8"); !ok || !reXID8.MatchString(r.XID8) {
		bad("xid8")
	} else if _, err := strconv.ParseUint(r.XID8, 10, 64); err != nil {
		bad("xid8")
	}
	if r.CreatedAt, ok = text("created_at"); !ok || !reLedgerTime.MatchString(r.CreatedAt) {
		bad("created_at")
	}
	return r
}

// XIDLow32 is the low 32 bits of the ledger's xid8 (the frame's BEGIN xid when the row was written
// by begin_action in that transaction).
func (r LedgerRow) XIDLow32() (uint32, bool) {
	v, err := strconv.ParseUint(r.XID8, 10, 64)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func validID[K ids.Kind](s string) bool {
	_, err := ids.Parse[K](s)
	return err == nil
}
