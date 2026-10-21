// Package debezium reads Debezium change events (JSON) into feed transactions (TY-05, ADR-114).
//
// Input is JSON lines. Each line is one of: a change event's value (the envelope with before,
// after, source, op), the same wrapped by the JSON converter's schema ({"schema":…,"payload":…}), a
// {"key":…,"value":…} pair, a transaction metadata event (status BEGIN or END), or a JSON array of
// any of these (the HTTP sink's batch mode). Schema change events and heartbeats are skipped.
//
// Transactions are grouped by the event's transaction.id (provide.transaction.metadata), else the
// source's gtid (MySQL) or txId (Postgres). A transaction is complete only when the feed holds its
// BEGIN and END and exactly the events END counts, in order with no gap or conflicting repeat;
// anything less is reported as Tx.Incomplete and never compared.
//
// It reads; it never connects to anything.
package debezium

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/witness/feed"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// MaxLine is the longest event line read (16 MiB).
const MaxLine = 16 << 20

// MaxInput is the most a whole feed may hold after gunzip (1 GiB). The CLI stops reading past it
// (audit L-17), so a gzip bomb or an endless stdin can't fill memory or run forever.
const MaxInput = 1 << 30

// DefaultLedgerTable is the table name (in any database) whose inserts name an action.
const DefaultLedgerTable = "deedglass_ledger"

// Options are what the events don't say.
type Options struct {
	// PK maps a table ("shop.orders") to its primary-key columns. A {"key":…} pair's key wins.
	PK map[string][]string
	// LedgerTable is the ledger table, qualified ("shop.deedglass_ledger") or bare (any database).
	// Empty means DefaultLedgerTable.
	LedgerTable string
}

// Feed is a parsed feed: its source and its transactions in the order the feed first showed them.
type Feed struct {
	Source  feed.Source
	Txs     []feed.Tx
	Events  int // change events read (snapshot reads and skipped events aside)
	Skipped int // schema changes, heartbeats, snapshot reads, tombstones
}

// unavailable is Debezium's placeholder for a value the source didn't send (unchanged TOAST in
// Postgres, a LOB in Oracle).
const unavailable = "__debezium_unavailable_value"

type source struct {
	Connector string          `json:"connector"`
	Name      string          `json:"name"`
	TsMs      int64           `json:"ts_ms"`
	Snapshot  json.RawMessage `json:"snapshot"`
	DB        string          `json:"db"`
	Schema    string          `json:"schema"`
	Table     string          `json:"table"`
	ServerID  json.RawMessage `json:"server_id"`
	GTID      *string         `json:"gtid"`
	TxID      json.RawMessage `json:"txId"`
	File      string          `json:"file"`
	Pos       json.RawMessage `json:"pos"`
	Row       *int            `json:"row"`
	LSN       json.RawMessage `json:"lsn"`
}

type txMeta struct {
	ID                  string `json:"id"`
	TotalOrder          int    `json:"total_order"`
	DataCollectionOrder int    `json:"data_collection_order"`
}

type event struct {
	Before      map[string]json.RawMessage `json:"before"`
	After       map[string]json.RawMessage `json:"after"`
	Source      *source                    `json:"source"`
	Op          string                     `json:"op"`
	Transaction *txMeta                    `json:"transaction"`
	// Transaction metadata events.
	Status          string `json:"status"`
	ID              string `json:"id"`
	EventCount      *int   `json:"event_count"`
	DataCollections []struct {
		DataCollection string `json:"data_collection"`
		EventCount     int    `json:"event_count"`
	} `json:"data_collections"`
	TsMs int64 `json:"ts_ms"`
	// Schema change events.
	DDL *string `json:"ddl"`

	key map[string]json.RawMessage
	raw []byte
}

// builder collects one transaction.
type builder struct {
	id      string
	begin   bool
	end     *event
	seen    map[int][]byte      // total_order → the event's bytes
	keyCols map[string][]string // table → the key columns an event's key named
	noOrder bool                // an event without transaction metadata
	events  []*event
}

// Read parses a whole feed.
func Read(r io.Reader, o Options) (*Feed, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	f := &Feed{}
	byID := map[string]*builder{}
	var order []string
	line := 0
	add := func(ev *event) error {
		switch {
		case ev.Status == "BEGIN" || ev.Status == "END":
			b := get(byID, &order, ev.ID)
			if ev.Status == "BEGIN" {
				b.begin = true
			} else {
				b.end = ev
			}
			return nil
		case ev.DDL != nil, ev.Source == nil, ev.Op == "", ev.Op == "m":
			f.Skipped++ // schema changes, heartbeats, logical messages
			return nil
		case ev.Op == "r" || snapshotting(ev.Source.Snapshot):
			f.Skipped++ // a snapshot read is the table as it was, not a change
			return nil
		}
		if f.Source.Method == "" {
			f.Source = sourceOf(ev.Source.Connector)
		}
		id, ordered := txID(ev)
		if id == "" {
			// No transaction id at all: each event stands alone and is incomplete by definition.
			id = "event:" + strconv.Itoa(line)
		}
		b := get(byID, &order, id)
		if len(ev.key) > 0 {
			b.keyCols[tableOf(ev.Source)] = sortedKeys(ev.key)
		}
		if !ordered {
			b.noOrder = true
		} else {
			n := ev.Transaction.TotalOrder
			if prev, dup := b.seen[n]; dup {
				if !bytes.Equal(canonJSON(prev), canonJSON(ev.raw)) {
					return fmt.Errorf("line %d: transaction %s has two different events at position %d", line, id, n)
				}
				return nil // the same event delivered again (at-least-once)
			}
			b.seen[n] = ev.raw
		}
		b.events = append(b.events, ev)
		f.Events++
		return nil
	}
	for sc.Scan() {
		line++
		text := bytes.TrimSpace(sc.Bytes())
		if len(text) == 0 {
			continue
		}
		evs, err := parseLine(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		for _, ev := range evs {
			if err := add(ev); err != nil {
				return nil, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("line %d is longer than %d MiB", line+1, MaxLine>>20)
		}
		return nil, err
	}
	for _, id := range order {
		b := byID[id]
		if len(b.events) == 0 {
			continue // BEGIN and END with no change the feed kept (another database, a snapshot)
		}
		tx, err := build(b, o, f.Source)
		if err != nil {
			return nil, err
		}
		f.Txs = append(f.Txs, tx)
	}
	return f, nil
}

func get(m map[string]*builder, order *[]string, id string) *builder {
	b, ok := m[id]
	if !ok {
		b = &builder{id: id, seen: map[int][]byte{}, keyCols: map[string][]string{}}
		m[id] = b
		*order = append(*order, id)
	}
	return b
}

func snapshotting(raw json.RawMessage) bool {
	s := strings.Trim(string(raw), `"`)
	return s != "" && s != "false" && s != "null"
}

// txID is the event's transaction: its metadata id (ordered), else gtid or txId (unordered).
func txID(ev *event) (string, bool) {
	if ev.Transaction != nil && ev.Transaction.ID != "" {
		return ev.Transaction.ID, ev.Transaction.TotalOrder > 0
	}
	if s := ev.Source; s != nil {
		if s.GTID != nil && *s.GTID != "" {
			return *s.GTID, false
		}
		if t := strings.Trim(string(s.TxID), `"`); t != "" && t != "null" {
			return t, false
		}
	}
	return "", false
}

// parseLine reads one line: an event, a wrapped event, a key/value pair, or an array of them.
func parseLine(b []byte) ([]*event, error) {
	if b[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, fmt.Errorf("not JSON: %w", err)
		}
		var out []*event
		for _, x := range arr {
			ev, err := parseOne(x)
			if err != nil {
				return nil, err
			}
			if ev != nil {
				out = append(out, ev)
			}
		}
		return out, nil
	}
	ev, err := parseOne(b)
	if err != nil || ev == nil {
		return nil, err
	}
	return []*event{ev}, nil
}

func parseOne(b []byte) (*event, error) {
	var top map[string]json.RawMessage
	if err := decode(b, &top); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if top == nil {
		return nil, nil // a tombstone (null value)
	}
	var key map[string]json.RawMessage
	if v, ok := top["value"]; ok && len(top) <= 3 {
		if k, ok := top["key"]; ok {
			key = unwrapKey(k)
		}
		b = v
		if string(bytes.TrimSpace(v)) == "null" {
			return nil, nil
		}
		top = nil
		if err := decode(b, &top); err != nil {
			return nil, fmt.Errorf("the value isn't a JSON object: %w", err)
		}
	}
	if p, ok := top["payload"]; ok {
		if _, ok := top["schema"]; ok {
			b = p
			if string(bytes.TrimSpace(p)) == "null" {
				return nil, nil
			}
		}
	}
	ev := &event{}
	if err := decode(b, ev); err != nil {
		return nil, fmt.Errorf("not a Debezium event: %w", err)
	}
	ev.key, ev.raw = key, append([]byte(nil), b...)
	return ev, nil
}

// unwrapKey reads a key that may itself carry a schema.
func unwrapKey(k json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if decode(k, &m) != nil || m == nil {
		return nil
	}
	if p, ok := m["payload"]; ok {
		if _, ok := m["schema"]; ok {
			var inner map[string]json.RawMessage
			if decode(p, &inner) == nil {
				return inner
			}
		}
	}
	return m
}

func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// canonJSON is b with object keys sorted, for comparing a repeated event.
func canonJSON(b []byte) []byte {
	var v any
	if decode(b, &v) != nil {
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

// Databases Debezium has connectors for, by source.connector.
var databases = map[string]string{
	"mysql": "MySQL", "mariadb": "MariaDB", "postgresql": "PostgreSQL", "sqlserver": "SQL Server",
	"oracle": "Oracle", "db2": "Db2", "informix": "Informix", "mongodb": "MongoDB", "spanner": "Spanner",
	"vitess": "Vitess",
}

func sourceOf(connector string) feed.Source {
	name, ok := databases[connector]
	if !ok {
		name = connector
	}
	return feed.Source{Via: "Debezium", Database: name, Method: "debezium." + connector}
}

// tableOf is the event's table: schema.table where the source has schemas, else db.table.
func tableOf(s *source) string {
	if s.Schema != "" {
		return s.Schema + "." + s.Table
	}
	if s.DB != "" {
		return s.DB + "." + s.Table
	}
	return s.Table
}

func (o Options) isLedger(table string) bool {
	lt := o.LedgerTable
	if lt == "" {
		lt = DefaultLedgerTable
	}
	if strings.Contains(lt, ".") {
		return table == lt
	}
	_, name, _ := strings.Cut(table, ".")
	return name == lt
}

// build turns one transaction's events into a feed.Tx.
func build(b *builder, o Options, src feed.Source) (feed.Tx, error) {
	tx := feed.Tx{ID: b.id, Counts: map[string]l2.OpCounts{}, Tables: map[string]l2.TableInfo{}}
	sort.SliceStable(b.events, func(i, j int) bool {
		ti, tj := b.events[i].Transaction, b.events[j].Transaction
		if ti == nil || tj == nil {
			return false
		}
		return ti.TotalOrder < tj.TotalOrder
	})
	tx.Incomplete = completeness(b)
	h := sha256.New()
	cols := map[string]map[string]bool{}
	kinds := map[string]map[string]string{} // table → column → json kind
	fullOld := map[string]bool{}
	pk := map[string][]string{}
	for i, ev := range b.events {
		h.Write(canonJSON(ev.raw))
		h.Write([]byte{'\n'})
		s := ev.Source
		if i == 0 {
			tx.Position = position(s)
			tx.ServerID = strings.Trim(string(s.ServerID), `"`)
		}
		if s.TsMs > 0 {
			tx.CommitTime = time.UnixMilli(s.TsMs).UTC()
		}
		table := tableOf(s)
		if o.isLedger(table) && ev.Op == "c" {
			tx.Ledger = append(tx.Ledger, ledgerRow(ev.After))
			continue
		}
		c := tx.Counts[table]
		switch ev.Op {
		case "c":
			c.Insert++
		case "u":
			c.Update++
		case "d":
			c.Delete++
		case "t":
			c.Truncate++
			tx.Counts[table] = c
			continue
		default:
			return tx, fmt.Errorf("transaction %s: unknown op %q", b.id, ev.Op)
		}
		tx.Counts[table] = c
		if cols[table] == nil {
			cols[table], kinds[table] = map[string]bool{}, map[string]string{}
			fullOld[table] = true
		}
		if keys := b.keyCols[table]; len(keys) > 0 {
			pk[table] = keys
		} else if keys := o.PK[table]; len(keys) > 0 {
			pk[table] = keys
		}
		ch := l2.Change{Seq: len(tx.Changes) + 1, Op: map[string]string{"c": "I", "u": "U", "d": "D"}[ev.Op], Table: table}
		ch.Old = values(ev.Before, cols[table], kinds[table])
		ch.New = values(ev.After, cols[table], kinds[table])
		if ev.Op != "c" && len(ev.Before) == 0 {
			fullOld[table] = false
		}
		if ev.Op == "u" && len(ev.Before) > 0 {
			ch.Changed = changed(ch.Old, ch.New)
		}
		tx.Changes = append(tx.Changes, ch)
		tx.Total++
	}
	for t, cs := range cols {
		ti := l2.TableInfo{PK: pk[t], Identity: "d"}
		if fullOld[t] {
			ti.Identity = "f"
		}
		for c := range cs {
			ti.Columns = append(ti.Columns, c)
		}
		sort.Strings(ti.Columns)
		for _, c := range ti.Columns {
			ti.TypeOIDs = append(ti.TypeOIDs, oidOf(kinds[t][c]))
		}
		tx.Tables[t] = ti
	}
	// Keys come last: the primary key is known once every event of the table is read.
	for i := range tx.Changes {
		ch := &tx.Changes[i]
		img := ch.Old
		if ch.Op == "I" || len(img) == 0 {
			img = ch.New
		}
		ch.Key = keyOf(img, pk[ch.Table])
	}
	tx.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return tx, nil
}

// completeness says why the feed holds only part of a transaction, or "".
func completeness(b *builder) string {
	switch {
	case b.noOrder:
		return "the feed carries no transaction metadata; turn on provide.transaction.metadata"
	case !b.begin && b.end == nil:
		return "the feed has neither its BEGIN nor its END"
	case !b.begin:
		return "the feed doesn't have its BEGIN"
	case b.end == nil:
		return "the feed ends before its END"
	}
	n := 0
	if b.end.EventCount != nil {
		n = *b.end.EventCount
	}
	orders := make([]int, 0, len(b.seen))
	for k := range b.seen {
		orders = append(orders, k)
	}
	sort.Ints(orders)
	if len(orders) > 0 && orders[0] != 1 {
		return fmt.Sprintf("it starts at event %d of %d", orders[0], max(n, orders[len(orders)-1]))
	}
	for i, k := range orders {
		if k != i+1 {
			return fmt.Sprintf("event %d of %d is missing", i+1, max(n, orders[len(orders)-1]))
		}
	}
	if len(orders) != n {
		return fmt.Sprintf("it holds %d of the %d events its END counts", len(orders), n)
	}
	// After a restart inside a transaction, Debezium re-sends BEGIN, skips the rows it had already
	// delivered and numbers what is left from 1, so END agrees with a partial feed. On MySQL the
	// first change of a transaction is row 0 of its first rows event; anything else is a resume.
	if first := b.events[0].Source; first != nil && first.Connector == "mysql" && first.Row != nil && *first.Row != 0 {
		return fmt.Sprintf("it resumes at row %d of a binlog event", *first.Row)
	}
	perTable := map[string]int{}
	for _, ev := range b.events {
		perTable[tableOf(ev.Source)]++
	}
	for _, dc := range b.end.DataCollections {
		if perTable[dc.DataCollection] != dc.EventCount {
			return fmt.Sprintf("its END counts %d events in %s and the feed holds %d", dc.EventCount, dc.DataCollection, perTable[dc.DataCollection])
		}
	}
	return ""
}

// position is where the source logged the change: binlog file and position, or the LSN.
func position(s *source) string {
	if s.File != "" {
		return s.File + ":" + strings.Trim(string(s.Pos), `"`)
	}
	if l := strings.Trim(string(s.LSN), `"`); l != "" && l != "null" {
		return "lsn " + l
	}
	return ""
}

func sortedKeys(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// values turns an image into l2 values and records each column and its JSON kind.
func values(img map[string]json.RawMessage, cols map[string]bool, kinds map[string]string) map[string]l2.Value {
	if len(img) == 0 {
		return nil
	}
	out := make(map[string]l2.Value, len(img))
	for c, raw := range img {
		cols[c] = true
		v, kind := valueOf(raw)
		out[c] = v
		if kind == "" {
			continue
		}
		switch prev := kinds[c]; {
		case prev == "":
			kinds[c] = kind
		case prev != kind:
			kinds[c] = "mixed"
		}
	}
	return out
}

// valueOf is one JSON value as the text a person reads, and its kind for the type check.
func valueOf(raw json.RawMessage) (l2.Value, string) {
	t := bytes.TrimSpace(raw)
	switch {
	case len(t) == 0 || string(t) == "null":
		return l2.Value{N: true}, ""
	case t[0] == '"':
		var s string
		if json.Unmarshal(t, &s) != nil {
			return l2.Value{X: hex.EncodeToString(t)}, "mixed"
		}
		if s == unavailable {
			return l2.Value{U: true}, ""
		}
		return l2.Value{T: &s}, "string"
	case string(t) == "true" || string(t) == "false":
		s := string(t)
		return l2.Value{T: &s}, "bool"
	case t[0] == '{' || t[0] == '[':
		s := string(canonJSON(t))
		return l2.Value{T: &s}, "mixed"
	}
	s := string(t)
	if isInteger(s) {
		return l2.Value{T: &s}, "integer"
	}
	return l2.Value{T: &s}, "number"
}

func isInteger(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Type OIDs witness/l2's key and value checks read: the JSON kind decides, since a feed without a
// schema says no more.
func oidOf(kind string) uint32 {
	switch kind {
	case "integer":
		return 20 // int8
	case "string":
		return 25 // text
	case "bool":
		return 16
	}
	return 0
}

func changed(old, new map[string]l2.Value) []string {
	var out []string
	for c, nv := range new {
		ov, ok := old[c]
		if !ok || !same(ov, nv) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func same(a, b l2.Value) bool {
	if a.N != b.N || a.U != b.U || a.X != b.X || a.D != b.D {
		return false
	}
	if (a.T == nil) != (b.T == nil) {
		return false
	}
	return a.T == nil || *a.T == *b.T
}

func keyOf(img map[string]l2.Value, pk []string) map[string]*string {
	if len(pk) == 0 || img == nil {
		return nil
	}
	k := make(map[string]*string, len(pk))
	for _, c := range pk {
		v, ok := img[c]
		if !ok {
			return nil
		}
		if v.N {
			k[c] = nil
			continue
		}
		s, ok := v.Text()
		if !ok {
			return nil
		}
		k[c] = &s
	}
	return k
}

// ledgerRow reads a ledger insert's columns.
func ledgerRow(after map[string]json.RawMessage) l2.LedgerRow {
	str := func(c string) string {
		v, _ := valueOf(after[c])
		if v.T == nil {
			return ""
		}
		return *v.T
	}
	lr := l2.LedgerRow{ActionID: str("action_id"), OrgID: str("org_id"), AgentID: str("agent_id"), SQLDigest: str("sql_digest")}
	if ih := str("intent_hash"); ih != "" {
		lr.IntentHash = &ih
	}
	if lr.ActionID == "" {
		lr.Invalid = "no action_id"
	}
	return lr
}
