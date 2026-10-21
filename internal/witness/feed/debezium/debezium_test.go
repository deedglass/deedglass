package debezium

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/feed"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	fh, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRecordedScene is the live scene's outcomes on its recording (make debezium-it RECORD=1):
// MySQL 8.4.6 and Debezium Server 3.3.1, the HTTP sink's bodies as JSON lines.
func TestRecordedScene(t *testing.T) {
	f, err := Read(bytes.NewReader(readFixture(t, "mysql-trigger.jsonl.gz")), sceneOptions())
	if err != nil {
		t.Fatal(err)
	}
	checkScene(t, f)
}

// TestResumedFeed is a real Debezium Server restart inside a 301-row transaction: it re-sent
// BEGIN, skipped the 48 rows it had delivered, numbered the rest from 1 and sent an END that
// counts 252, so the metadata alone looks whole. The rows of shop.orders and the ledger are gone
// from the feed; comparing would say "MySQL committed 0" in shop.orders. It must be a gap.
func TestResumedFeed(t *testing.T) {
	f, err := Read(bytes.NewReader(readFixture(t, "mysql-resumed.jsonl.gz")), sceneOptions())
	if err != nil {
		t.Fatal(err)
	}
	var cut *feed.Tx
	for i := range f.Txs {
		if f.Txs[i].Incomplete != "" {
			cut = &f.Txs[i]
		}
	}
	if cut == nil || cut.Incomplete != "it resumes at row 48 of a binlog event" || cut.Total != 252 {
		t.Fatalf("the resumed transaction isn't flagged: %+v", f.Txs)
	}
	rep := evaluate(t, f, l2.Action{}, sqlA, 1, cut.ID)
	d := rep.Outcome.Decision
	if d.Verdict != witness.Unverifiable || slices.Contains(codes(rep), "missing_change") || !slices.Contains(codes(rep), "feed_incomplete") {
		t.Fatalf("%s %v", d.Verdict, codes(rep))
	}
}

// TestCutFeedNeverDecides cuts the recording at many points: a transaction cut short is a gap,
// never a violation or a pass (rule 3).
func TestCutFeedNeverDecides(t *testing.T) {
	lines := bytes.Split(bytes.TrimSpace(readFixture(t, "mysql-trigger.jsonl.gz")), []byte("\n"))
	for cut := 1; cut < len(lines); cut += 7 {
		in := bytes.Join(append(append([][]byte{}, lines[:cut]...), lines[min(cut+3, len(lines)):]...), []byte("\n"))
		for _, part := range [][]byte{bytes.Join(lines[:cut], []byte("\n")), bytes.Join(lines[cut:], []byte("\n")), in} {
			f, err := Read(bytes.NewReader(part), sceneOptions())
			if err != nil {
				t.Fatalf("cut %d: %v", cut, err)
			}
			for _, tx := range f.Txs {
				if tx.Incomplete == "" {
					continue
				}
				rep := evaluate(t, f, l2.Action{}, sqlA, 1, tx.ID)
				if v := rep.Outcome.Decision.Verdict; v != witness.Unverifiable {
					t.Fatalf("cut %d: an incomplete transaction (%s) gave %s %v", cut, tx.Incomplete, v, codes(rep))
				}
			}
		}
	}
}

const (
	evBegin = `{"status":"BEGIN","id":"g:1","event_count":null,"data_collections":null,"ts_ms":1}`
	evEnd2  = `{"status":"END","id":"g:1","event_count":2,"data_collections":[{"data_collection":"shop.orders","event_count":2}],"ts_ms":1}`
	evU1    = `{"before":{"id":1,"status":"open"},"after":{"id":1,"status":"shipped"},"source":{"connector":"mysql","db":"shop","table":"orders","ts_ms":1792571657878,"gtid":"g:1","file":"mysql-bin.000003","pos":10,"row":0,"snapshot":"false"},"transaction":{"id":"g:1","total_order":1,"data_collection_order":1},"op":"u"}`
	evU2    = `{"before":{"id":2,"status":"open"},"after":{"id":2,"status":"shipped"},"source":{"connector":"mysql","db":"shop","table":"orders","ts_ms":1792571657878,"gtid":"g:1","file":"mysql-bin.000003","pos":10,"row":1,"snapshot":"false"},"transaction":{"id":"g:1","total_order":2,"data_collection_order":2},"op":"u"}`
)

func read(t *testing.T, o Options, lines ...string) *Feed {
	t.Helper()
	f, err := Read(strings.NewReader(strings.Join(lines, "\n")), o)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestCompleteness(t *testing.T) {
	o := Options{PK: map[string][]string{"shop.orders": {"id"}}}
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"whole", []string{evBegin, evU1, evU2, evEnd2}, ""},
		{"no begin", []string{evU1, evU2, evEnd2}, "the feed doesn't have its BEGIN"},
		{"no end", []string{evBegin, evU1, evU2}, "the feed ends before its END"},
		{"neither", []string{evU1, evU2}, "the feed has neither its BEGIN nor its END"},
		{"missing first", []string{evBegin, evU2, evEnd2}, "it starts at event 2 of 2"},
		{"short", []string{evBegin, evU1, evEnd2}, "it holds 1 of the 2 events its END counts"},
		{"repeat", []string{evBegin, evU1, evU1, evU2, evEnd2}, ""},
		{"no metadata", []string{strings.Replace(evU1, `"transaction":{"id":"g:1","total_order":1,"data_collection_order":1},`, "", 1)},
			"the feed carries no transaction metadata; turn on provide.transaction.metadata"},
		{"resume", []string{evBegin, strings.Replace(evU1, `"row":0`, `"row":5`, 1), evU2, evEnd2}, "it resumes at row 5 of a binlog event"},
		{"collection counts", []string{evBegin, evU1, evU2, strings.Replace(evEnd2, `"data_collection":"shop.orders","event_count":2`, `"data_collection":"shop.orders","event_count":1},{"data_collection":"shop.items","event_count":1`, 1)},
			"its END counts 1 events in shop.orders and the feed holds 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := read(t, o, c.lines...)
			if len(f.Txs) != 1 || f.Txs[0].Incomplete != c.want {
				t.Fatalf("%+v", f.Txs)
			}
		})
	}
	// The same position with a different event is a broken feed, not a repeat.
	if _, err := Read(strings.NewReader(strings.Join([]string{evBegin, evU1, strings.Replace(evU1, "shipped", "lost", 1)}, "\n")), o); err == nil {
		t.Fatal("two different events at one position were accepted")
	}
}

func TestShapes(t *testing.T) {
	wrapped := `{"schema":{"type":"struct"},"payload":` + evU1 + `}`
	pair := `{"key":{"id":1},"value":` + evU1 + `}`
	batch := "[" + evBegin + "," + evU1 + "," + evU2 + "," + evEnd2 + "]"
	ddl := `{"source":{"connector":"mysql","db":"shop"},"ddl":"CREATE TABLE x (a int)","tableChanges":[]}`
	snap := strings.Replace(strings.Replace(evU1, `"op":"u"`, `"op":"r"`, 1), `"snapshot":"false"`, `"snapshot":"true"`, 1)
	tomb := `{"key":{"id":1},"value":null}`

	f := read(t, Options{}, evBegin, wrapped, pair, evU2, evEnd2, ddl, snap, tomb)
	if len(f.Txs) != 1 || f.Txs[0].Incomplete != "" || f.Txs[0].Total != 2 || f.Skipped != 2 {
		t.Fatalf("%+v skipped %d", f.Txs, f.Skipped)
	}
	tx := f.Txs[0]
	// The pair's key names the primary key; every change gets it.
	if ti := tx.Tables["shop.orders"]; !slices.Equal(ti.PK, []string{"id"}) || ti.Identity != "f" || ti.TypeOf("id") != 20 || ti.TypeOf("status") != 25 {
		t.Fatalf("%+v", ti)
	}
	if k := tx.Changes[1].Key["id"]; k == nil || *k != "2" || !slices.Equal(tx.Changes[0].Changed, []string{"status"}) {
		t.Fatalf("%+v", tx.Changes)
	}
	if tx.Position != "mysql-bin.000003:10" || tx.CommitTime.IsZero() || !strings.HasPrefix(tx.Digest, "sha256:") {
		t.Fatalf("%+v", tx)
	}
	if b := read(t, Options{PK: map[string][]string{"shop.orders": {"id"}}}, batch); len(b.Txs) != 1 || b.Txs[0].Incomplete != "" {
		t.Fatalf("batch: %+v", b.Txs)
	}
	if _, err := Read(strings.NewReader("{not json"), Options{}); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestValues(t *testing.T) {
	ins := `{"before":null,"after":{"id":9,"note":"__debezium_unavailable_value","price":"12.50","ok":true,"gone":null,"doc":{"b":1,"a":2}},"source":{"connector":"postgresql","schema":"public","db":"app","table":"items","ts_ms":1,"txId":77,"lsn":123,"snapshot":"false"},"transaction":{"id":"77:123","total_order":1,"data_collection_order":1},"op":"c"}`
	begin := `{"status":"BEGIN","id":"77:123"}`
	end := `{"status":"END","id":"77:123","event_count":1,"data_collections":[{"data_collection":"public.items","event_count":1}]}`
	f := read(t, Options{PK: map[string][]string{"public.items": {"id"}}}, begin, ins, end)
	if f.Source.Label() != "Proven · via Debezium (PostgreSQL)" || len(f.Txs) != 1 {
		t.Fatalf("%+v", f)
	}
	ch := f.Txs[0].Changes[0]
	if ch.Table != "public.items" || ch.Op != "I" || !ch.New["note"].U || !ch.New["gone"].N || *ch.New["price"].T != "12.50" ||
		*ch.New["ok"].T != "true" || *ch.New["doc"].T != `{"a":2,"b":1}` || f.Txs[0].Position != "lsn 123" {
		t.Fatalf("%+v", ch)
	}
}

func TestLedgerTable(t *testing.T) {
	led := `{"before":null,"after":{"action_id":"act_x","org_id":"org_x","agent_id":"agt_x","sql_digest":"sha256:x"},"source":{"connector":"mysql","db":"shop","table":"deedglass_ledger","ts_ms":1,"gtid":"g:1","row":0,"snapshot":"false"},"transaction":{"id":"g:1","total_order":1,"data_collection_order":1},"op":"c"}`
	u1 := strings.Replace(strings.Replace(evU1, `"total_order":1`, `"total_order":2`, 1), `"row":0`, `"row":0`, 1)
	end := `{"status":"END","id":"g:1","event_count":2,"data_collections":[{"data_collection":"shop.deedglass_ledger","event_count":1},{"data_collection":"shop.orders","event_count":1}]}`
	for _, lt := range []string{"", "deedglass_ledger", "shop.deedglass_ledger"} {
		f := read(t, Options{LedgerTable: lt}, evBegin, led, u1, end)
		tx := f.Txs[0]
		if tx.Incomplete != "" || len(tx.Ledger) != 1 || tx.Ledger[0].ActionID != "act_x" || tx.Total != 1 || tx.Counts["shop.deedglass_ledger"].Rows() != 0 {
			t.Fatalf("%q: %+v", lt, tx)
		}
	}
	// Another table name: the ledger rows are ordinary changes.
	f := read(t, Options{LedgerTable: "other.deedglass_ledger"}, evBegin, led, u1, end)
	if len(f.Txs[0].Ledger) != 0 || f.Txs[0].Total != 2 {
		t.Fatalf("%+v", f.Txs[0])
	}
}

func TestNoMatchAndNamed(t *testing.T) {
	f := read(t, Options{PK: map[string][]string{"shop.orders": {"id"}}}, evBegin, evU1, evU2, evEnd2)
	rep := evaluate(t, f, l2.Action{}, "UPDATE items SET a = 1 WHERE id = 1", 1, "")
	if rep.Attribution != feed.AttrNone || !slices.Equal(codes(rep), []string{"feed_no_match"}) || rep.Outcome.Decision.Verdict != witness.Unverifiable {
		t.Fatalf("%s %v", rep.Attribution, codes(rep))
	}
	if !strings.HasPrefix(rep.Outcome.Detail.Summary, "The Debezium feed from MySQL holds no transaction") {
		t.Fatalf("%q", rep.Outcome.Detail.Summary)
	}
	rep = evaluate(t, f, l2.Action{}, "UPDATE orders SET status = 'shipped' WHERE id IN (1, 2)", 2, "g:1")
	if rep.Attribution != feed.AttrNamed || rep.Outcome.Decision.Verdict != witness.Verified || !slices.Contains(codes(rep), "any_writer") {
		t.Fatalf("%s %s %v", rep.Attribution, rep.Outcome.Decision.Verdict, codes(rep))
	}
	rep = evaluate(t, f, l2.Action{}, "UPDATE orders SET status = 'shipped' WHERE id = 1", 1, "g:404")
	if rep.Attribution != feed.AttrNone {
		t.Fatalf("%s", rep.Attribution)
	}
}

func TestRelabel(t *testing.T) {
	s := feed.Source{Via: "Debezium", Database: "MySQL", Method: "debezium.mysql"}
	cases := map[string]string{
		"Postgres changed 301 rows in public.orders. The agent reported 1.":      "MySQL changed 301 rows in public.orders. The agent reported 1.",
		"Postgres's change log didn't show this action within 10s.":              "The Debezium feed from MySQL didn't show this action within 10s.",
		"public.orders isn't watched, so Postgres's change log doesn't show it.": "public.orders isn't watched, so the Debezium feed from MySQL doesn't show it.",
	}
	for in, want := range cases {
		if got := feed.Relabel(witness.Reason{Message: in}, s).Message; got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
}
