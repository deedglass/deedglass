package debezium

import (
	"slices"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/feed"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// The trigger scene on MySQL (deploy/debezium/mysql/01-shop.sql), as TestMySQLTriggerLive runs it
// and testdata/mysql-trigger.jsonl.gz records it:
//
//	A: the agent ships order 42 and writes its ledger row in the same transaction (the feed
//	   carries the action id). The trigger ships the other 300 open orders of batch 7.
//	B: the agent ships order 43 with no ledger row (any writer); the trigger ships 300 more.
//	C: the agent changes order 7's total with its ledger row; no trigger fires.
const (
	sceneAction  = "act_01k82m4v9q5n3d7f8g2h6j0kxp"
	sceneActionC = "act_01k82m4v9q5n3d7f8g2h6j0kxq"
	sceneOrg     = "org_01k82m4v9q5n3d7f8g2h6j0kxr"
	sceneAgent   = "agt_01k82m4v9q5n3d7f8g2h6j0kxs"
	sceneDigest  = "sha256:5d1b3f0a9c2e4b6d8f0a1c3e5b7d9f1a3c5e7b9d1f3a5c7e9b1d3f5a7c9e1b3d"

	sqlA = "UPDATE orders SET status = 'shipped' WHERE id = 42"
	sqlB = "UPDATE orders SET status = 'shipped' WHERE id = 43"
	sqlC = "UPDATE orders SET total_cents = 2000 WHERE id = 7"
)

const sceneSQL = `START TRANSACTION;
INSERT INTO deedglass_ledger (action_id, org_id, agent_id, sql_digest) VALUES ('` + sceneAction + `', '` + sceneOrg + `', '` + sceneAgent + `', '` + sceneDigest + `');
` + sqlA + `;
COMMIT;
` + sqlB + `;
START TRANSACTION;
INSERT INTO deedglass_ledger (action_id, org_id, agent_id, sql_digest) VALUES ('` + sceneActionC + `', '` + sceneOrg + `', '` + sceneAgent + `', '` + sceneDigest + `');
` + sqlC + `;
COMMIT;
`

func sceneOptions() Options {
	return Options{PK: map[string][]string{"shop.orders": {"id"}, "shop.shipments": {"order_id"}}}
}

func one(n int64) *int64 { return &n }

func evaluate(t *testing.T, f *Feed, action l2.Action, sql string, reported int64, tx string) feed.Report {
	t.Helper()
	rep, err := feed.Evaluate(feed.Input{Source: f.Source, Txs: f.Txs, Action: action, Tx: tx,
		Derive:  l2.DeriveInput{Tool: l2.ToolExecute, SQL: sql, DefaultSchema: "shop", Reported: one(reported)},
		Config:  l2.Config{DiffRows: l2.DefaultDiffRows, DiffColumns: l2.DefaultDiffColumns},
		Witness: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func codes(rep feed.Report) []string {
	var out []string
	for _, r := range rep.Outcome.Decision.Reasons {
		out = append(out, string(r.Code))
	}
	return out
}

// checkScene asserts the scene's outcomes on a feed read from the live run or its recording.
func checkScene(t *testing.T, f *Feed) {
	t.Helper()
	if f.Source.Label() != "Proven · via Debezium (MySQL)" || f.Source.Method != "debezium.mysql" {
		t.Fatalf("source %+v", f.Source)
	}
	var scene []feed.Tx
	for _, tx := range f.Txs {
		if tx.Incomplete != "" {
			t.Fatalf("transaction %s incomplete: %s", tx.ID, tx.Incomplete)
		}
		if tx.Total > 0 {
			scene = append(scene, tx)
		}
	}
	if len(scene) != 3 || scene[0].Total != 301 || scene[1].Total != 301 || scene[2].Total != 1 {
		t.Fatalf("want 301, 301 and 1 row changes, got %d transactions", len(scene))
	}

	// A: bound by its ledger row; MySQL committed 301 rows, the agent reported 1.
	a := evaluate(t, f, l2.Action{ID: sceneAction, Org: sceneOrg, Agent: sceneAgent, SQLDigest: sceneDigest}, sqlA, 1, "")
	d := a.Outcome.Decision
	if d.Verdict != witness.Mismatch || d.Level != witness.L2 || d.Method != "debezium.mysql" || a.Attribution != feed.AttrAction {
		t.Fatalf("A: %s %s %s %s %v", d.Level, d.Verdict, d.Method, a.Attribution, codes(a))
	}
	if !slices.Contains(codes(a), "unexpected_table") || slices.Contains(codes(a), "any_writer") || len(a.Txs) != 1 || a.Txs[0].Total != 301 {
		t.Fatalf("A: %v", codes(a))
	}
	if a.Outcome.Detail.Summary != "MySQL also changed 300 rows in shop.shipments, which the statement doesn't touch." {
		t.Fatalf("A summary: %q", a.Outcome.Detail.Summary)
	}

	// B: no ledger row, so the transactions touching shop.orders are compared, with the caveat.
	b := evaluate(t, f, l2.Action{}, sqlB, 1, "")
	if b.Outcome.Decision.Verdict != witness.Mismatch || b.Attribution != feed.AttrAnyWriter {
		t.Fatalf("B: %s %s %v", b.Outcome.Decision.Verdict, b.Attribution, codes(b))
	}
	if !slices.Contains(codes(b), "any_writer") || !slices.Contains(codes(b), "unexpected_table") {
		t.Fatalf("B: %v", codes(b))
	}

	// C: bound by its ledger row; one row, as the statement said.
	c := evaluate(t, f, l2.Action{ID: sceneActionC}, sqlC, 1, "")
	if c.Outcome.Decision.Verdict != witness.Verified || c.Attribution != feed.AttrAction || !slices.Equal(codes(c), []string{"footprint_matched"}) {
		t.Fatalf("C: %s %s %v", c.Outcome.Decision.Verdict, c.Attribution, codes(c))
	}
	if c.Outcome.Detail.Summary != "MySQL committed 1 row in shop.orders, as the statement said." {
		t.Fatalf("C summary: %q", c.Outcome.Detail.Summary)
	}

	// A named by a ledger row from another org: the cross-check says which field.
	x := evaluate(t, f, l2.Action{ID: sceneAction, Org: "org_01k82m4v9q5n3d7f8g2h6j0kxt"}, sqlA, 1, "")
	if !slices.Contains(codes(x), "ledger_mismatch") {
		t.Fatalf("cross-check: %v", codes(x))
	}
}
