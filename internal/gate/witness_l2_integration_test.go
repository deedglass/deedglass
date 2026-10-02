//go:build integration

package gate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// --- L2 in the Gate (S21 §3.2, §3.8), with the decoder's frames written by the test ---

// l2Resolver makes fake.write a write witnessed at L2: its executor answers L2Target (the
// hero's statement) and can write a frame, or fail, while it runs.
type l2Resolver struct {
	inner   Resolver
	w       *world
	conID   string
	before  atomic.Bool // write a bound frame before Execute returns (the frame-before-B1 race)
	unknown atomic.Bool // the call's answer is lost after the frame committed (V0's E06)
}

func (r *l2Resolver) Resolve(ctx context.Context, org ids.OrgID, q string) (Executor, connector.ToolSpec, string, error) {
	ex, spec, ctype, err := r.inner.Resolve(ctx, org, q)
	if err != nil || q != "fake.write" {
		return ex, spec, ctype, err
	}
	return &l2Exec{Executor: ex, r: r}, spec, "postgres", nil
}

type l2Exec struct {
	Executor
	r *l2Resolver
}

const l2SQL = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"

func (e *l2Exec) L2Target(connector.Call) (*connector.L2Target, error) {
	return &connector.L2Target{ConnectorID: e.r.conID, Tool: "execute", SQL: l2SQL, DefaultSchema: "public",
		SQLDigest: "sha256:" + strings.Repeat("ab", 32), Watched: []string{"public.orders"}, Timeout: 2 * time.Second}, nil
}

func (e *l2Exec) L2Triggers(context.Context, []string, bool) (map[string][]string, error) {
	return nil, nil
}

func (e *l2Exec) Execute(ctx context.Context, call connector.Call) (connector.Result, error) {
	res, err := e.Executor.Execute(ctx, call)
	res.Meta = map[string]any{"command": "UPDATE", "rows_affected": int64(1)}
	if e.r.before.Load() || e.r.unknown.Load() {
		e.r.w.frame(call.ActionID.String(), 1, "0/1000")
	}
	if e.r.unknown.Load() {
		return connector.Result{}, errors.New("the connection dropped after COMMIT")
	}
	return res, err
}

// l2World is a world whose fake.write waits for L2 on a witnessed connector with a running decoder.
func l2World(t *testing.T) (*world, *l2Resolver) {
	w := newWorld(t, worldOpts{})
	con := ids.New[ids.ConnectorKind]().String()
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return store.InsertConnector(ctx, tx, store.Connector{ID: con, OrgID: w.org.String(), Type: "postgres", Name: "fake", Description: "L2 test",
			Config: json.RawMessage(`{}`), ConfigHash: "sha256:" + strings.Repeat("0", 64), Status: "active", CreatedBy: "system"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `INSERT INTO deedglass.decoder_slots (org_id, connector_id, slot_name, status) VALUES ($1, $2, $3, 'running')`,
		w.org, con, "deedglass_"+strings.ToLower(con[4:])); err != nil {
		t.Fatal(err)
	}
	r := &l2Resolver{inner: w.p.o.Resolver, w: w, conID: con}
	w.p.o.Resolver = r
	return w, r
}

// frame stores a bound frame for action: n rows of public.orders updated (order 42 first), one
// ledger row naming the action, at commitLSN.
func (w *world) frame(action string, n int, commitLSN string) {
	w.t.Helper()
	var agent string
	_ = w.e.Admin(w.t).QueryRow(bg, `SELECT agent_id FROM deedglass.actions WHERE id = $1`, action).Scan(&agent)
	var changes []map[string]any
	for i := 0; i < n; i++ {
		id := 42
		if i > 0 {
			id = i
		}
		k := fmt.Sprint(id)
		changes = append(changes, map[string]any{"seq": i + 1, "op": "U", "table": "public.orders", "key": map[string]any{"id": k},
			"old": map[string]any{"id": map[string]string{"t": k}, "status": map[string]string{"t": "paid"}},
			"new": map[string]any{"id": map[string]string{"t": k}, "status": map[string]string{"t": "shipped"}}, "changed": []string{"status"}})
	}
	cb, _ := json.Marshal(changes)
	lb, _ := json.Marshal([]map[string]any{{"action_id": action, "org_id": w.org.String(), "agent_id": agent, "intent_hash": nil,
		"sql_digest": "sha256:" + strings.Repeat("ab", 32), "xid8": "777", "created_at": "2026-10-07T13:12:09Z"}})
	tb := `{"public.orders":{"relid":16400,"pk":["id"],"identity":"f","columns":["id","status"],"type_oids":[23,25]}}`
	counts := fmt.Sprintf(`{"public.orders":{"insert":0,"update":%d,"delete":0,"truncate":0}}`, n)
	var con string
	_ = w.e.Admin(w.t).QueryRow(bg, `SELECT connector_id FROM deedglass.decoder_slots LIMIT 1`).Scan(&con)
	now := time.Now()
	xid := int64(777)
	digest := "sha256:" + strings.Repeat("9d", 32)
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.InsertFrames(ctx, tx, []store.DecodedFrame{{OrgID: w.org.String(), ConnectorID: con, SystemID: "7690386503164422802",
			TimelineID: 1, CommitLSN: &commitLSN, EndLSN: &commitLSN, Xid: &xid, CommitTime: &now, Class: "bound", ActionID: &action,
			Ledger: lb, Markers: []string{action}, Tables: json.RawMessage(tb), Counts: json.RawMessage(counts), Changes: cb,
			ChangeCount: n, ChangesDigest: &digest}})
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// view reads an action's witness through the ledger's detail.
func (w *world) witnessOf(id string) *ledger.ActionWitness {
	w.t.Helper()
	d, err := w.led.GetAction(bg, w.org, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return d.Witness
}

func (w *world) waitState(id, state string) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var st string
		_ = w.e.Admin(w.t).QueryRow(bg, `SELECT state FROM deedglass.actions WHERE id = $1`, id).Scan(&st)
		if st == state {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("%s is %s, not %s", id, st, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func noWait(r ActionRequest) ActionRequest { r.Wait = 0; return r }

func reasonCodes(w *ledger.ActionWitness, level string) string {
	var out []string
	if w.Reasons != nil {
		for _, r := range *w.Reasons {
			if level == "" || string(r.Level) == level {
				out = append(out, string(r.Code))
			}
		}
	}
	return strings.Join(out, ",")
}

func TestL2FrameArrives(t *testing.T) {
	w, _ := l2World(t)
	a := w.must(0, noWait(req("fake.write", `{"rows":1}`)))
	w.waitState(a.ID, "witnessing")
	w.frame(a.ID, 1, "0/2000")
	w.p.L2Evidence(bg, w.org, a.ID)
	w.waitState(a.ID, "sealed")
	wt := w.witnessOf(a.ID)
	if wt.Level != "L2" || wt.Verdict != "VERIFIED" || *wt.Method != "postgres.pgoutput" || *wt.Summary != "Postgres committed 1 row in public.orders, as the statement said." {
		t.Fatalf("%+v %s", wt, *wt.Summary)
	}
	if !strings.Contains(string(wt.Detail), `"0/2000"`) || !strings.Contains(string(wt.Detail), `"7690386503164422802"`) {
		t.Fatalf("detail %s", wt.Detail)
	}
	// The hero: 51 rows where the statement named one.
	b := w.must(1, noWait(req("fake.write", `{"rows":1}`)))
	w.waitState(b.ID, "witnessing")
	w.frame(b.ID, 51, "0/3000")
	w.p.L2Evidence(bg, w.org, b.ID)
	w.waitState(b.ID, "sealed")
	if wt := w.witnessOf(b.ID); wt.Verdict != "MISMATCH" || wt.Level != "L2" || *wt.Summary != "Postgres changed 51 rows in public.orders. The agent reported 1." ||
		reasonCodes(wt, "L2") != "row_count_exceeded,unexpected_key" {
		t.Fatalf("hero %+v", wt)
	}
}

// TestL2FrameBeforeB1 is the race of S21 §3.2: the decoder persists the frame before Tx B1
// commits, so OnBound finds the action executing; the post-commit check finishes it.
func TestL2FrameBeforeB1(t *testing.T) {
	w, r := l2World(t)
	r.before.Store(true)
	a := w.must(0, req("fake.write", `{"rows":1}`))
	if a.State != "sealed" || a.Witness == nil || a.Witness.Level != "L2" || a.Witness.Verdict != "VERIFIED" {
		t.Fatalf("%s %+v", a.State, a.Witness)
	}
}

// TestL2UnknownExecution is V0's E06: the answer was lost after COMMIT, so L0 can't tell, and
// Postgres's change log says what happened.
func TestL2UnknownExecution(t *testing.T) {
	w, r := l2World(t)
	r.unknown.Store(true)
	a := w.must(0, req("fake.write", `{"rows":1}`))
	w.waitState(a.ID, "sealed")
	wt := w.witnessOf(a.ID)
	if wt.Level != "L2" || wt.Verdict != "VERIFIED" || !strings.Contains(reasonCodes(wt, "L0"), "outcome_unknown") {
		t.Fatalf("%+v %s", wt, reasonCodes(wt, ""))
	}
}

func TestL2SlotLostAndMissing(t *testing.T) {
	w, r := l2World(t)
	con, _ := ids.Parse[ids.ConnectorKind](r.conID)
	a := w.must(0, noWait(req("fake.write", `{"rows":1}`)))
	b := w.must(1, noWait(req("fake.write", `{"rows":1}`)))
	w.waitState(a.ID, "witnessing")
	w.waitState(b.ID, "witnessing")
	w.p.L2Missing(bg, w.org, b.ID)
	w.p.L2SlotLost(bg, w.org, con, "slot_missing")
	w.waitState(a.ID, "sealed")
	w.waitState(b.ID, "sealed")
	if got := reasonCodes(w.witnessOf(a.ID), "L2"); got != "slot_dropped" {
		t.Errorf("slot lost: %s", got)
	}
	if got := reasonCodes(w.witnessOf(b.ID), "L2"); got != "no_evidence" {
		t.Errorf("missing: %s", got)
	}
	// A halted decoder at plan time: the gap now, no wait.
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.decoder_slots SET status = 'halted', status_reason = 'publication_missing'`); err != nil {
		t.Fatal(err)
	}
	c := w.must(2, req("fake.write", `{"rows":1}`))
	if c.State != "sealed" || reasonCodes(c.Witness, "L2") != "decoder_halted" {
		t.Fatalf("halted: %s %+v", c.State, c.Witness)
	}
	// A dropped decoder: no L2 at all.
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.decoder_slots SET status = 'dropped'`); err != nil {
		t.Fatal(err)
	}
	d := w.must(0, req("fake.write", `{"rows":1}`))
	if d.State != "sealed" || reasonCodes(d.Witness, "L2") != "" {
		t.Fatalf("dropped: %s %+v", d.State, d.Witness)
	}
}

// TestL2Amendments is S21 §3.8: late evidence never edits a deed; a different verdict is an
// amendment deed (at most three), the same one a note.
func TestL2Amendments(t *testing.T) {
	w, _ := l2World(t)
	stop := runWorker(t, w)
	defer stop()
	a := w.must(0, req("fake.write", `{"rows":1}`)) // waits out l2_timeout (2 s), then the job seals it
	w.waitState(a.ID, "sealed")
	wt := w.witnessOf(a.ID)
	if wt.Level != "L0" || wt.Verdict != "VERIFIED" || reasonCodes(wt, "L2") != "l2_timeout" {
		t.Fatalf("timeout: %+v %s", wt, reasonCodes(wt, ""))
	}
	original := *w.witnessOf(a.ID)
	deeds := func() []string {
		var out []string
		rows, _ := w.e.Admin(t).Query(bg, `SELECT kind || ':' || COALESCE(amends, '') FROM deedglass.deeds WHERE action_id = $1 ORDER BY seq`, a.ID)
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			out = append(out, s)
		}
		rows.Close()
		return out
	}
	actionDeed := *a.Deed
	// 1. The late frame: an amendment deed, L2 · VERIFIED, amending the action deed.
	w.frame(a.ID, 1, "0/4000")
	w.p.L2Evidence(bg, w.org, a.ID)
	if got := deeds(); len(got) != 2 || got[1] != "amendment:"+actionDeed {
		t.Fatalf("deeds %v", got)
	}
	if wt := w.witnessOf(a.ID); wt.Level != "L2" || wt.Verdict != "VERIFIED" || len(wt.History) != 2 || *wt.History[0].Reason != "l2_timeout" {
		t.Fatalf("after: %+v", wt)
	}
	// The same evidence again changes nothing.
	w.p.L2Evidence(bg, w.org, a.ID)
	if got := deeds(); len(got) != 2 {
		t.Fatalf("a repeat sealed again: %v", got)
	}
	// 2. A second transaction claims the action: MISMATCH, a second amendment.
	w.frame(a.ID, 1, "0/5000")
	w.p.L2Evidence(bg, w.org, a.ID)
	if wt := w.witnessOf(a.ID); wt.Verdict != "MISMATCH" || !strings.Contains(reasonCodes(wt, "L2"), "multiple_transactions") || len(deeds()) != 3 {
		t.Fatalf("multiple: %+v %v", wt, deeds())
	}
	// 3. A third: the same verdict, so a note (l2_late_confirmed) and no deed.
	w.frame(a.ID, 1, "0/6000")
	w.p.L2Evidence(bg, w.org, a.ID)
	if got := deeds(); len(got) != 3 {
		t.Fatalf("an unchanged verdict sealed: %v", got)
	}
	var notes int
	_ = w.e.Admin(t).QueryRow(bg, `SELECT count(*) FROM deedglass.action_events WHERE action_id = $1 AND data ->> 'reason' = 'l2_late_confirmed'`, a.ID).Scan(&notes)
	if notes != 1 {
		t.Fatalf("%d l2_late_confirmed notes", notes)
	}
	// The original deed is unchanged.
	var orig []byte
	_ = w.e.Admin(t).QueryRow(bg, `SELECT envelope FROM deedglass.deeds WHERE id = $1`, actionDeed).Scan(&orig)
	if !strings.Contains(string(orig), `"verdict":"VERIFIED"`) || !strings.Contains(string(orig), `"level":"L0"`) || original.Level != "L0" {
		t.Fatalf("the action deed changed: %s", orig)
	}
	// 4. The limit: an action with three amendments gets an alert, not a fourth deed.
	b := w.must(1, req("fake.write", `{"rows":1}`))
	w.waitState(b.ID, "sealed")
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.actions SET witness_amendments = 3 WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	lc := w.e.Admin(t)
	if _, err := lc.Exec(bg, "LISTEN deedglass_events"); err != nil {
		t.Fatal(err)
	}
	w.frame(b.ID, 1, "0/7000")
	w.p.L2Evidence(bg, w.org, b.ID)
	alerted := false
	for !alerted {
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		n, err := lc.WaitForNotification(ctx)
		cancel()
		if err != nil {
			break
		}
		alerted = strings.Contains(n.Payload, `"kind":"amendment_limit"`)
	}
	var nb int
	_ = w.e.Admin(t).QueryRow(bg, `SELECT count(*) FROM deedglass.deeds WHERE action_id = $1`, b.ID).Scan(&nb)
	if !alerted || nb != 1 {
		t.Fatalf("limit: alerted %v, %d deeds", alerted, nb)
	}
	// The chain holds every amendment.
	c, err := w.led.CheckChain(bg, w.org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || c.Status != ledger.CheckIntact {
		t.Fatalf("chain %+v %v", c, err)
	}
}
