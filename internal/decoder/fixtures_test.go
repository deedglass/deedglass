package decoder

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Synthetic streams for the pure tests.

const (
	relLedger = 16401
	relOrders = 16410
	relWide   = 16420
	relPlain  = 16430 // REPLICA IDENTITY DEFAULT
)

var (
	ledgerCols = []wcol{{"action_id", 25, true, -1}, {"org_id", 25, true, -1}, {"agent_id", 25, true, -1}, {"intent_hash", 25, true, -1},
		{"sql_digest", 25, true, -1}, {"xid8", 5069, true, -1}, {"created_at", 1184, true, -1}}
	orderCols = []wcol{{"id", 23, true, -1}, {"status", 25, true, -1}, {"total_cents", 23, true, -1}, {"note", 25, true, -1}}
	wideCols  = []wcol{{"id", 23, true, -1}, {"counter", 23, true, -1}, {"blob", 25, true, -1}}
	plainCols = []wcol{{"id", 23, true, -1}, {"status", 25, false, -1}}
	t0        = time.Date(2026, 10, 7, 9, 12, 9, 114000000, time.UTC)
	digestA   = "sha256:" + strings.Repeat("a", 64)
)

type fx struct {
	org, agent, action string
	xid                uint32
}

func newFx(xid uint32) fx {
	return fx{org: ids.New[ids.OrgKind]().String(), agent: ids.New[ids.AgentKind]().String(),
		action: ids.New[ids.ActionKind]().String(), xid: xid}
}

func (f fx) ledgerRow() []wval {
	return []wval{tv(f.action), tv(f.org), tv(f.agent), vNull, tv(digestA), tv(fmt.Sprint(uint64(1)<<32 + uint64(f.xid))),
		tv("2026-10-07 09:12:09.114+00")}
}

// msgs is a stream of raw messages with LSNs.
type msgs []Raw

func (m *msgs) add(lsn LSN, b []byte) { *m = append(*m, Raw{LSN: lsn, Data: b}) }

// boundTx is S17's transaction: ledger row, marker, then one UPDATE of orders 42.
func boundTx(f fx, base LSN) msgs {
	var m msgs
	m.add(base, encBegin(base+0x100, t0, f.xid))
	m.add(base+0x10, encRelation(relLedger, "deedglass", "ledger", 'f', ledgerCols))
	m.add(base+0x10, encInsert(relLedger, f.ledgerRow()))
	m.add(base+0x20, encMessage(true, base+0x28, "deedglass", []byte(f.action)))
	m.add(base+0x30, encRelation(relOrders, "public", "orders", 'f', orderCols))
	m.add(base+0x30, encUpdate(relOrders, 'O', []wval{tv("42"), tv("paid"), tv("1042"), vNull}, []wval{tv("42"), tv("shipped"), tv("1042"), vNull}))
	m.add(base+0x100, encCommit(base+0x100, base+0x130, t0))
	return m
}

// assemble runs SafeParse and an Assembler over m.
func assemble(t testing.TB, m msgs, o AssembleOptions) *Batch {
	t.Helper()
	a := NewAssembler(o)
	for _, r := range m {
		msg, err := SafeParse(r.Data)
		if err != nil {
			t.Fatalf("SafeParse(%x): %v", r.Data, err)
		}
		if err := a.Add(r.LSN, msg); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	b, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// syntheticCorpus is one valid message of every type (and both old-tuple kinds).
func syntheticCorpus() [][]byte {
	f := newFx(7461)
	return [][]byte{
		encBegin(0x3E65020, t0, 7461),
		encCommit(0x3E65020, 0x3E65050, t0),
		encOrigin(0x1000, "upstream_a"),
		encRelation(relOrders, "public", "orders", 'f', orderCols),
		encType(16500, "public", "order_status"),
		encInsert(relLedger, f.ledgerRow()),
		encUpdate(relOrders, 'O', []wval{tv("42"), tv("paid"), tv("1042"), vNull}, []wval{tv("42"), tv("shipped"), tv("1042"), vNull}),
		encUpdate(relOrders, 'K', []wval{tv("42"), vNull, vNull, vNull}, []wval{tv("43"), tv("paid"), tv("1042"), vNull}),
		encUpdate(relPlain, 0, nil, []wval{tv("1"), tv("x")}),
		encUpdate(relWide, 'O', []wval{tv("1"), tv("0"), tv(strings.Repeat("x", 3000))}, []wval{tv("1"), tv("1"), vUnchanged}),
		encDelete(relOrders, 'O', []wval{tv("42"), tv("paid"), tv("1042"), vNull}),
		encDelete(relPlain, 'K', []wval{tv("1"), vNull}),
		encTruncate(1, relOrders, relWide),
		encMessage(true, 0x3E64FD0, "deedglass", []byte(f.action)),
		encMessage(false, 0x3E64FD0, "deedglass", []byte("hb1:con_x:1:00")),
	}
}

// capturedCorpus loads testdata/decoder/pgoutput/*.bin (real messages from PG 16/17/18, captured
// by TestCaptureCorpus); nil when the directory isn't there.
func capturedCorpus(t testing.TB) map[string][]byte {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "decoder", "pgoutput")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string][]byte{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
