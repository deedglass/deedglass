//go:build integration

package decoder_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// S20 §6's integration table. Each test builds a customer database with S17's planner
// (pgtest.Init), never a hand-written copy, and a control-plane database with an org and a
// witnessed postgres connector; the decoder runs as a real supervisor.

type change struct {
	Seq     int                      `json:"seq"`
	Op      string                   `json:"op"`
	Table   string                   `json:"table"`
	Key     map[string]*string       `json:"key"`
	Old     map[string]decoder.Value `json:"old"`
	New     map[string]decoder.Value `json:"new"`
	Changed []string                 `json:"changed"`
}

func changesOf(t *testing.T, f store.DecodedFrame) []change {
	t.Helper()
	var cs []change
	if err := json.Unmarshal(f.Changes, &cs); err != nil {
		t.Fatal(err)
	}
	return cs
}

func countsOf(t *testing.T, f store.DecodedFrame) map[string]decoder.OpCounts {
	t.Helper()
	m := map[string]decoder.OpCounts{}
	if err := json.Unmarshal(f.Counts, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func text(v decoder.Value) string {
	if v.Text == nil {
		return "<nil>"
	}
	return *v.Text
}

// The agent's UPDATE of order 42: one bound frame, U 1, key {"id":"42"}, changed [status],
// marker_lsn stored; the slot was created after the publication check and its deed sealed.
func TestBoundUpdate(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	id := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 42`)
	f := r.waitFrame(id, "bound")
	cs := changesOf(t, f)
	if len(cs) != 1 || cs[0].Op != "U" || cs[0].Table != "public.orders" || cs[0].Key["id"] == nil || *cs[0].Key["id"] != "42" ||
		!slices.Equal(cs[0].Changed, []string{"status", "updated_at"}) && !slices.Equal(cs[0].Changed, []string{"status"}) {
		t.Fatalf("%+v", cs)
	}
	if text(cs[0].Old["status"]) != "paid" || text(cs[0].New["status"]) != "shipped" {
		t.Fatalf("images: %+v", cs[0])
	}
	if c := countsOf(t, f)["public.orders"]; c.Update != 1 || c.Insert != 0 {
		t.Fatalf("counts %+v", c)
	}
	if f.MarkerLSN == nil || f.CommitLSN == nil || f.ChangeCount != 1 || f.ChangesDigest == nil || len(f.Markers) != 1 || f.Markers[0] != id {
		t.Fatalf("%+v", f)
	}
	var ledgerRows []decoder.LedgerRow
	_ = json.Unmarshal(f.Ledger, &ledgerRows)
	if len(ledgerRows) != 1 || ledgerRows[0].ActionID != id || ledgerRows[0].OrgID != r.org.String() {
		t.Fatalf("ledger %s", f.Ledger)
	}
	s := r.slot()
	if s.SlotState != "created" || s.SystemID == nil || s.CheckpointLSN == nil || s.Frames["bound"] < 1 {
		t.Fatalf("slot row %+v", s)
	}
	if s.SlotName != setup.SlotName(r.con.String()) {
		t.Fatalf("slot %s", s.SlotName)
	}
	eventually(t, 10*time.Second, func() string { // the hook runs just after the commit the test saw
		if bound, _, _ := r.hooks.snapshot(); !slices.Contains(bound, id+":bound") {
			return fmt.Sprintf("OnBound didn't run: %v", bound)
		}
		return ""
	})
	// decoder.slot_created was sealed.
	if n := r.adminDeeds(ledger.AdminDecoderSlotCreated); n != 1 {
		t.Fatalf("%d decoder.slot_created deeds", n)
	}
	// The decoder object.
	var view decoder.View
	if err := r.e.DB.OrgReadTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		recent, err := store.RecentFrames(ctx, tx, r.con.String(), 4)
		view = decoder.BuildView(s, r.name, recent)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if view.Health != "healthy" || view.Server.Version == nil || len(view.Recent) == 0 || view.Recent[0].Class != "bound" {
		t.Fatalf("%+v", view)
	}
}

func (r *rig) adminDeeds(action string) int {
	r.t.Helper()
	c := r.e.Admin(r.t)
	var n int
	if err := c.QueryRow(bg, `SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = $2 AND deed_id IS NOT NULL`,
		r.org.String(), action).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// INSERT, UPDATE and DELETE on a REPLICA IDENTITY FULL table: exact old and new images, and
// 2^53+1 survives as text (spike S2).
func TestImagesExact(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	big := "9007199254740993"
	ins := r.act(`INSERT INTO public.refunds (id, order_id, amount_cents, reason, created_at) VALUES (9007199254740993, 1, 500, 'damaged', '2026-10-07 09:12:09.114+00')`)
	upd := r.act(`UPDATE public.refunds SET amount_cents = 450 WHERE id = 9007199254740993`)
	del := r.act(`DELETE FROM public.refunds WHERE id = 9007199254740993`)
	ci := changesOf(t, r.waitFrame(ins, "bound"))
	cu := changesOf(t, r.waitFrame(upd, "bound"))
	cd := changesOf(t, r.waitFrame(del, "bound"))
	if ci[0].Op != "I" || text(ci[0].New["id"]) != big || text(ci[0].New["created_at"]) != "2026-10-07 09:12:09.114+00" || ci[0].Old != nil {
		t.Fatalf("insert %+v", ci[0])
	}
	if cu[0].Op != "U" || text(cu[0].Old["amount_cents"]) != "500" || text(cu[0].New["amount_cents"]) != "450" ||
		!slices.Equal(cu[0].Changed, []string{"amount_cents"}) || *cu[0].Key["id"] != big {
		t.Fatalf("update %+v", cu[0])
	}
	if cd[0].Op != "D" || text(cd[0].Old["reason"]) != "damaged" || cd[0].New != nil || *cd[0].Key["id"] != big {
		t.Fatalf("delete %+v", cd[0])
	}
}

// D01 (spike T4): a transactional deedglass marker with no ledger row is a forged marker: stored,
// alerted (decoder.alert), never bound. D02 (T3/T3b): a non-transactional one, and a forged
// heartbeat, are forged_marker_nontx.
func TestForgedMarkers(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	fake := ids.New[ids.ActionKind]().String()
	r.admin(`SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, fake)
	eventually(t, 20*time.Second, func() string {
		if n := r.count(`class = 'forged_marker' AND action_id IS NULL`); n != 1 {
			return fmt.Sprintf("%d forged_marker frames", n)
		}
		return ""
	})
	eventually(t, 10*time.Second, func() string {
		if r.events.find(ledger.EventDecoderAlert, func(e ledger.Event) bool { return e.Kind != nil && *e.Kind == "forged_marker" }) == nil {
			return "no decoder.alert for the forged marker"
		}
		return ""
	})
	nontx := ids.New[ids.ActionKind]().String()
	r.admin(`SELECT pg_catalog.pg_logical_emit_message(false, 'deedglass', $1::text)`, nontx)
	forged := fmt.Sprintf("hb1:%s:%d:%s", r.con, time.Now().Add(time.Hour).UnixMilli(), strings.Repeat("ab", 32))
	r.admin(`SELECT pg_catalog.pg_logical_emit_message(false, 'deedglass', $1::text)`, forged)
	// T3: a non-transactional marker from a transaction that then rolls back is still delivered.
	r.admin(`DO $$ BEGIN PERFORM pg_catalog.pg_logical_emit_message(false, 'deedglass', 'from an aborted tx'); RAISE EXCEPTION 'abort'; EXCEPTION WHEN OTHERS THEN NULL; END $$`)
	eventually(t, 20*time.Second, func() string {
		if n := r.count(`class = 'forged_marker_nontx' AND nontx`); n != 3 {
			return fmt.Sprintf("%d forged_marker_nontx frames", n)
		}
		return ""
	})
	if n := r.count(`class = 'bound'`); n != 0 {
		t.Fatalf("%d bound frames", n)
	}
	if s := r.slot(); s.Frames["forged_marker"] != 1 || s.Frames["forged_marker_nontx"] != 3 || s.Frames["heartbeats"] == 0 {
		t.Fatalf("counters %v", s.Frames)
	}
}

// D03 (spike T2b): a marker inside a rolled-back savepoint never arrives: nothing is stored.
// D04 (T4b): a second marker after begin_action's in the same transaction is marker_count.
func TestSavepointAndExtraMarker(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	tx, _ := c.Begin(bg)
	_, _ = tx.Exec(bg, "SAVEPOINT s")
	_, _ = tx.Exec(bg, `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, ids.New[ids.ActionKind]().String())
	_, _ = tx.Exec(bg, "ROLLBACK TO SAVEPOINT s")
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	id := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 7`)
	// The extra marker: as the agent, after its statement.
	extra := ids.New[ids.ActionKind]().String()
	ac, err := pgx.Connect(bg, r.cust.AgentDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer ac.Close(bg)
	atx, _ := ac.Begin(bg)
	if _, err := atx.Exec(bg, `SELECT * FROM deedglass.begin_action($1, $2, $3, NULL, $4)`, extra, r.org.String(), ids.New[ids.AgentKind]().String(),
		"sha256:"+strings.Repeat("1", 64)); err != nil {
		t.Fatal(err)
	}
	_, _ = atx.Exec(bg, `UPDATE public.orders SET status = 'shipped' WHERE id = 8`)
	_, _ = atx.Exec(bg, `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, extra)
	if err := atx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	r.waitFrame(id, "bound")
	f := r.waitFrame(extra, "marker_count")
	if len(f.Markers) != 2 {
		t.Fatalf("%+v", f)
	}
	if n := r.count(`class NOT IN ('bound', 'marker_count')`); n != 0 {
		t.Fatalf("%d other frames: the rolled-back marker left a trace", n)
	}
}

// D17: changes under a replication origin are decoded (origin any) and unattributed, with the
// origin's name in the flags.
func TestOriginChanges(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	if _, err := c.Exec(bg, `SELECT pg_catalog.pg_replication_origin_create('deedglass_it_origin')`); err != nil && !isPgCode(err, "42710") {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c2, err := pgx.Connect(bg, r.cust.AdminURL)
		if err == nil {
			_, _ = c2.Exec(bg, `SELECT pg_catalog.pg_replication_origin_drop('deedglass_it_origin')`)
			_ = c2.Close(bg)
		}
	})
	if _, err := c.Exec(bg, `SELECT pg_catalog.pg_replication_origin_session_setup('deedglass_it_origin')`); err != nil {
		t.Fatal(err)
	}
	tx, _ := c.Begin(bg)
	_, _ = tx.Exec(bg, `SELECT pg_catalog.pg_replication_origin_xact_setup('0/1000'::pg_lsn, pg_catalog.now())`)
	if _, err := tx.Exec(bg, `UPDATE public.bulk SET flag = true WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	_, _ = c.Exec(bg, `SELECT pg_catalog.pg_replication_origin_session_reset()`)
	eventually(t, 20*time.Second, func() string {
		if n := r.count(`class = 'unattributed' AND 'origin:deedglass_it_origin' = ANY (flags)`); n != 1 {
			return fmt.Sprintf("%d origin frames; flags so far: %v", n, flagsOf(r))
		}
		return ""
	})
}

func flagsOf(r *rig) [][]string {
	var out [][]string
	for _, f := range r.frames() {
		out = append(out, f.Flags)
	}
	return out
}

// Sensitive columns are stored as a digest only.
func TestSensitiveColumn(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	r.admin(`UPDATE public.customers SET email = 'new-address@acme.example' WHERE id = 3`)
	var f store.DecodedFrame
	eventually(t, 20*time.Second, func() string {
		for _, x := range r.frames() {
			if x.Class == "unattributed" {
				f = x
				return ""
			}
		}
		return "no frame"
	})
	f = r.fullFrame(f.ID)
	if strings.Contains(string(f.Changes), "new-address@acme.example") || strings.Contains(string(f.Changes), "c3@acme.example") {
		t.Fatal("a sensitive value was stored")
	}
	cs := changesOf(t, f)
	if cs[0].New["email"].Digest == "" || cs[0].Old["email"].Digest == "" || !slices.Equal(cs[0].Changed, []string{"email"}) {
		t.Fatalf("%+v", cs[0])
	}
}

func (r *rig) fullFrame(id string) store.DecodedFrame {
	r.t.Helper()
	c := r.e.Admin(r.t)
	var f store.DecodedFrame
	var changes []byte
	if err := c.QueryRow(bg, `SELECT id, class, changes FROM deedglass.decoded_txs WHERE id = $1`, id).Scan(&f.ID, &f.Class, &changes); err != nil {
		r.t.Fatal(err)
	}
	f.Changes = changes
	return f
}

// E01: the runner stops after the frames commit and before the slot advances; on restart the same
// frames come back and are stored once.
func TestCrashBetweenPersistAndAdvance(t *testing.T) {
	r := newRig(t, rigOpts{noStart: true})
	var once sync.Once
	fired := make(chan struct{})
	r.faults = decoder.Faults{AfterPersist: func(string) error {
		err := error(nil)
		if r.count(`class = 'bound'`) > 0 {
			once.Do(func() { close(fired); err = fmt.Errorf("fault: stop before advance") })
		}
		return err
	}}
	r.start()
	r.waitRunning()
	id := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 11`)
	select {
	case <-fired:
	case <-time.After(20 * time.Second):
		t.Fatal("the fault never fired")
	}
	// The runner retries; the same transaction is peeked again and deduplicated.
	id2 := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 12`)
	r.waitFrame(id2, "bound")
	if n := len(r.framesFor(id)); n != 1 {
		t.Fatalf("%d frames for the action, want 1", n)
	}
	if s := r.slot(); s.Frames["bound"] != 2 {
		t.Fatalf("counters %v", s.Frames)
	}
}

// The acceptance burst: 500 actions while the supervisor restarts; every action has exactly one
// bound frame.
func TestBurstAcrossRestart(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	const n = 500
	var wg sync.WaitGroup
	idsCh := make(chan string, n)
	for w := range 8 {
		wg.Go(func() {
			for i := w; i < n; i += 8 {
				idsCh <- r.act(`UPDATE public.bulk SET flag = NOT flag WHERE id = $1`, 1+i%1204)
				if i == n/2 {
					go r.restartAsync()
				}
			}
		})
	}
	wg.Wait()
	close(idsCh)
	r.waitRestarted()
	eventually(t, 60*time.Second, func() string {
		if c := r.count(`class = 'bound'`); c != n {
			return fmt.Sprintf("%d bound frames", c)
		}
		return ""
	})
	if d := r.count(`class = 'bound' AND action_id IN (SELECT DISTINCT action_id FROM deedglass.decoded_txs WHERE class = 'bound')`); d != n {
		t.Fatalf("%d", d)
	}
	c := r.e.Admin(t)
	var distinct int
	_ = c.QueryRow(bg, `SELECT count(DISTINCT action_id) FROM deedglass.decoded_txs WHERE connector_id = $1 AND class = 'bound'`, r.con.String()).Scan(&distinct)
	if distinct != n {
		t.Fatalf("%d distinct actions", distinct)
	}
}

var restartMu sync.Mutex
var restarted = make(chan struct{}, 1)

func (r *rig) restartAsync() {
	restartMu.Lock()
	defer restartMu.Unlock()
	r.restart()
	select {
	case restarted <- struct{}{}:
	default:
	}
}

func (r *rig) waitRestarted() {
	select {
	case <-restarted:
	case <-time.After(30 * time.Second):
		r.t.Fatal("the supervisor never restarted")
	}
}

// E10: a paused runner reads no heartbeat; past the deadline it is degraded (stalled) and
// decoder.degraded is published; unpaused, it recovers.
func TestStalled(t *testing.T) {
	var paused atomic.Bool
	r := newRig(t, rigOpts{cfg: func(c *decoder.Config) {
		c.HeartbeatDeadline = 3 * time.Second
		c.Paused = paused.Load
	}})
	r.waitRunning()
	paused.Store(true)
	eventually(t, 20*time.Second, func() string {
		s := r.slot()
		if s.Status != decoder.StatusDegraded || deref(s.StatusReason) != decoder.ReasonStalled {
			return "status " + s.Status + "/" + deref(s.StatusReason)
		}
		return ""
	})
	if r.events.find(ledger.EventDecoderDegraded, func(e ledger.Event) bool { return e.Reason != nil && *e.Reason == "stalled" }) == nil {
		eventually(t, 5*time.Second, func() string {
			if r.events.find(ledger.EventDecoderDegraded, func(e ledger.Event) bool { return e.Reason != nil && *e.Reason == "stalled" }) == nil {
				return "no decoder.degraded event"
			}
			return ""
		})
	}
	paused.Store(false)
	eventually(t, 20*time.Second, func() string {
		if s := r.slot(); s.Status != decoder.StatusRunning {
			return "status " + s.Status + "/" + deref(s.StatusReason)
		}
		return ""
	})
	eventually(t, 5*time.Second, func() string {
		if r.events.find(ledger.EventDecoderRecovered, nil) == nil {
			return "no decoder.recovered"
		}
		return ""
	})
}

// E11: hostile role settings never reach the decoder: the startup packet pins them, and the stored
// values are byte-identical to a baseline. The per-poll assert notices a changed session.
func TestHostileSettings(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	base := r.act(`INSERT INTO public.refunds (order_id, amount_cents, reason, created_at) VALUES (1, 100, 'baseline', '2026-10-07 09:12:09.114+00')`)
	fb := changesOf(t, r.waitFrame(base, "bound"))
	r.admin(fmt.Sprintf(`ALTER ROLE deedglass_witness IN DATABASE %s SET TimeZone = 'Pacific/Chatham'`, r.cust.Name))
	r.admin(fmt.Sprintf(`ALTER ROLE deedglass_witness IN DATABASE %s SET bytea_output = 'escape'`, r.cust.Name))
	r.admin(fmt.Sprintf(`ALTER ROLE deedglass_witness IN DATABASE %s SET DateStyle = 'SQL, DMY'`, r.cust.Name))
	r.admin(fmt.Sprintf(`ALTER ROLE deedglass_witness IN DATABASE %s SET IntervalStyle = 'sql_standard'`, r.cust.Name))
	r.restart()
	r.waitRunning()
	hostile := r.act(`INSERT INTO public.refunds (order_id, amount_cents, reason, created_at) VALUES (1, 100, 'baseline', '2026-10-07 09:12:09.114+00')`)
	fh := changesOf(t, r.waitFrame(hostile, "bound"))
	for _, col := range []string{"created_at", "amount_cents", "reason", "order_id"} {
		if text(fb[0].New[col]) != text(fh[0].New[col]) {
			t.Errorf("%s: baseline %q, hostile %q", col, text(fb[0].New[col]), text(fh[0].New[col]))
		}
	}
	if got := text(fh[0].New["created_at"]); got != "2026-10-07 09:12:09.114+00" {
		t.Fatalf("created_at %q", got)
	}
	var lr []decoder.LedgerRow
	_ = json.Unmarshal(r.framesFor(hostile)[0].Ledger, &lr)
	if len(lr) != 1 || lr[0].Invalid != "" || !strings.HasSuffix(lr[0].CreatedAt, "+00") {
		t.Fatalf("ledger row %+v", lr)
	}
	// The assert: a session whose setting changed is noticed before a peek.
	cfg, _, _ := r.target(bg, r.org, r.con)
	s, err := decoder.Connect(bg, cfg.Conn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(bg)
	sn, err := s.Snapshot(bg, "deedglass_pub")
	if err != nil || decoder.SettingsProblem(sn.Settings) != "" {
		t.Fatalf("%v %v", err, sn.Settings)
	}
	if _, err := s.Conn().Exec(bg, `SET TimeZone = 'Pacific/Chatham'`); err != nil {
		t.Fatal(err)
	}
	sn, _ = s.Snapshot(bg, "deedglass_pub")
	if p := decoder.SettingsProblem(sn.Settings); p != `TimeZone is "Pacific/Chatham"` {
		t.Fatalf("problem %q", p)
	}
}

// upto_nchanges is checked only at transaction boundaries: a 12,000-row transaction with
// batch_changes 5,000 comes back whole, as one frame.
func TestLargeTransactionWhole(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	r.admin(`INSERT INTO public.bulk (id) SELECT g FROM generate_series(100001, 112000) AS g`)
	var f store.DecodedFrame
	eventually(t, 30*time.Second, func() string {
		for _, x := range r.frames() {
			if x.Class == "unattributed" && x.ChangeCount == 12000 {
				f = x
				return ""
			}
		}
		return "no 12,000-change frame"
	})
	if c := countsOf(t, f)["public.bulk"]; c.Insert != 12000 || !slices.Contains(f.Flags, "images_truncated") {
		t.Fatalf("%+v %v", c, f.Flags)
	}
	if n := r.count(`class = 'unattributed'`); n != 1 {
		t.Fatalf("%d frames, want 1", n)
	}
	if n := len(changesOf(t, r.fullFrame(f.ID))); n != 1000 {
		t.Fatalf("%d images", n)
	}
}

// Finding 11: the publication is dropped, a row and its marker commit, then it is re-created. 16
// and 17 halt (publication_missing); 18 skips the row and keeps the marker, which becomes a
// publication_gap, never a forged marker.
func TestPublicationDropped(t *testing.T) {
	var paused atomic.Bool
	r := newRig(t, rigOpts{cfg: func(c *decoder.Config) { c.Paused = paused.Load }})
	r.waitRunning()
	before := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 20`)
	r.waitFrame(before, "bound")
	paused.Store(true)
	time.Sleep(300 * time.Millisecond)
	r.admin(`DROP PUBLICATION deedglass_pub`)
	gap := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 21`)
	r.admin(`CREATE PUBLICATION deedglass_pub FOR TABLE deedglass.ledger, public.bulk, public.customers, public.orders, public.refunds`)
	paused.Store(false)
	ver := r.serverVersion()
	if ver < 180000 {
		eventually(t, 30*time.Second, func() string {
			if s := r.slot(); s.Status != decoder.StatusHalted || deref(s.StatusReason) != decoder.ReasonPublicationMissing {
				return "status " + s.Status + "/" + deref(s.StatusReason)
			}
			return ""
		})
		_, _, lost := r.hooks.snapshot()
		if !slices.Contains(lost, decoder.ReasonPublicationMissing) {
			t.Fatalf("SlotLost %v", lost)
		}
		t.Logf("PG %d: halted publication_missing", ver)
		return
	}
	f := r.waitFrame(gap, "publication_gap")
	if n := r.count(`class = 'forged_marker'`); n != 0 {
		t.Fatalf("%d forged_marker frames", n)
	}
	t.Logf("PG %d: publication_gap %v", ver, f.Flags)
	after := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 22`)
	r.waitFrame(after, "bound")
}

func (r *rig) serverVersion() int {
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close(bg)
	var v int
	_ = c.QueryRow(bg, `SELECT current_setting('server_version_num')::int`).Scan(&v)
	return v
}

// ADR-045: with idle watched tables and 50 MB of WAL elsewhere, the checkpoint follows the flush
// position, so the slot holds under 1 MB after two polls.
func TestIdleAdvance(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	r.admin(`CREATE TABLE public.noise (id int, pad text)`)
	r.admin(`INSERT INTO public.noise SELECT g, pg_catalog.repeat('x', 1000) FROM generate_series(1, 50000) AS g`)
	var lag int64
	eventually(t, 20*time.Second, func() string {
		c, err := pgx.Connect(bg, r.cust.AdminURL)
		if err != nil {
			return err.Error()
		}
		defer c.Close(bg)
		_ = c.QueryRow(bg, `SELECT pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_current_wal_lsn(), confirmed_flush_lsn)::bigint
		    FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`, r.slot().SlotName).Scan(&lag)
		if lag >= 1<<20 {
			return fmt.Sprintf("lag %d bytes", lag)
		}
		return ""
	})
	t.Logf("after 50 MB of unpublished WAL: lag %d bytes", lag)
}

// Two supervisors: one runner. The holder stops; the other takes the lease within 20 s.
func TestTwoSupervisors(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	b := decoder.NewSupervisor(r.deps("it-b"), r.cfg)
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = b.Run(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(6 * time.Second)
	if (r.sup.Runner(r.con.String()) == nil) == (b.Runner(r.con.String()) == nil) {
		t.Fatal("want exactly one runner")
	}
	start := time.Now()
	r.stop()
	eventually(t, 25*time.Second, func() string {
		if b.Runner(r.con.String()) == nil {
			return "the second supervisor has no runner"
		}
		if s := r.slot(); s.Status != decoder.StatusRunning || deref(s.LeasedBy) != "it-b" {
			return "status " + s.Status + " leased by " + deref(s.LeasedBy)
		}
		return ""
	})
	took := time.Since(start)
	if took > 20*time.Second {
		t.Fatalf("the lease moved in %v", took)
	}
	t.Logf("the lease moved in %v", took)
	id := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 30`)
	r.waitFrame(id, "bound")
}

// A runner whose slot another session holds (the advisory lock) waits and names the holder.
func TestWaitingForHolder(t *testing.T) {
	r := newRig(t, rigOpts{noStart: true})
	c, err := pgx.Connect(bg, r.cust.AdminURL+"&application_name=other-deedglass")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	slot := setup.SlotName(r.con.String())
	var ok bool
	if err := c.QueryRow(bg, `SELECT pg_catalog.pg_try_advisory_lock(pg_catalog.hashtextextended('deedglass/decoder/' || $1, 0))`, slot).Scan(&ok); err != nil || !ok {
		t.Fatal(err, ok)
	}
	r.start()
	eventually(t, 20*time.Second, func() string {
		s := r.slot()
		if s.Status != decoder.StatusWaiting || !strings.Contains(string(s.StatusDetail), "other-deedglass") {
			return "status " + s.Status + " " + string(s.StatusDetail)
		}
		return ""
	})
	_, _ = c.Exec(bg, `SELECT pg_catalog.pg_advisory_unlock_all()`)
	r.waitRunning()
}

// DDL in the agent's transaction leaves one trace in the stream: relation_changed.
func TestRelationChanged(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	first := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 40`)
	r.waitFrame(first, "bound")
	id := ids.New[ids.ActionKind]().String()
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	tx, _ := c.Begin(bg)
	if _, err := tx.Exec(bg, `SELECT * FROM deedglass.begin_action($1, $2, $3, NULL, $4)`, id, r.org.String(), ids.New[ids.AgentKind]().String(),
		"sha256:"+strings.Repeat("2", 64)); err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(bg, `ALTER TABLE public.orders ADD COLUMN note text`)
	_, _ = tx.Exec(bg, `UPDATE public.orders SET note = 'x' WHERE id = 41`)
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	f := r.waitFrame(id, "bound")
	if !slices.Contains(f.Flags, "relation_changed:public.orders") {
		t.Fatalf("flags %v", f.Flags)
	}
}

// E04 and the drop acceptance: an admin drops the slot; actions in the gap get no frame; the next
// sync creates a new slot (a new baseline) and reconcile records the gap actions as missing. Both
// admin deeds are sealed.
func TestDropAndReslot(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitRunning()
	first := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 50`)
	r.waitFrame(first, "bound")
	r.stop()
	ctl := decoder.NewSupervisor(r.deps("it-ctl"), r.cfg)
	res, err := ctl.Drop(bg, r.org, r.con, deed.Ref{Type: "user", ID: ids.New[ids.UserKind]().String()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deed == "" || res.Slot != setup.SlotName(r.con.String()) {
		t.Fatalf("%+v", res)
	}
	var gap []string
	for i := range 3 {
		gap = append(gap, r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = $1`, 51+i))
	}
	r.start()
	r.waitRunning()
	after := r.act(`UPDATE public.orders SET status = 'shipped' WHERE id = 55`)
	r.waitFrame(after, "bound")
	r.sup.Runner(r.con.String()).ReconcileNow()
	for _, g := range gap {
		r.waitFrame(g, "missing")
	}
	if n := len(r.framesFor(first)); n != 1 {
		t.Fatalf("%d frames for the first action", n)
	}
	_, missing, lost := r.hooks.snapshot()
	if len(missing) != 3 || !slices.Contains(lost, decoder.ReasonDropped) {
		t.Fatalf("missing %v lost %v", missing, lost)
	}
	if r.adminDeeds(ledger.AdminDecoderSlotDropped) != 1 || r.adminDeeds(ledger.AdminDecoderSlotCreated) != 2 {
		t.Fatalf("deeds: dropped %d created %d", r.adminDeeds(ledger.AdminDecoderSlotDropped), r.adminDeeds(ledger.AdminDecoderSlotCreated))
	}
}

// The ledger function is S17's: a changed body halts the runner (ledger_function_changed).
func TestLedgerFunctionChanged(t *testing.T) {
	r := newRig(t, rigOpts{noStart: true})
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(bg)
	var src string
	_ = c.QueryRow(bg, `SELECT prosrc FROM pg_proc WHERE proname = 'begin_action'`).Scan(&src)
	body := strings.Replace(src, "RETURN QUERY", "-- tampered\n  RETURN QUERY", 1)
	stmt := "CREATE OR REPLACE FUNCTION deedglass.begin_action(p_action_id text, p_org_id text, p_agent_id text, p_intent_hash text, p_sql_digest text) " +
		"RETURNS TABLE (xid8 pg_catalog.xid8, marker_lsn pg_catalog.pg_lsn) LANGUAGE plpgsql VOLATILE SECURITY DEFINER " +
		"SET search_path = pg_catalog, pg_temp SET TimeZone = 'UTC' AS $fn$" + body + "$fn$"
	if _, err := c.Exec(bg, stmt); err != nil {
		t.Fatal(err)
	}
	r.start()
	eventually(t, 20*time.Second, func() string {
		if s := r.slot(); s.Status != decoder.StatusHalted || deref(s.StatusReason) != decoder.ReasonLedgerFunctionChanged {
			return "status " + s.Status + "/" + deref(s.StatusReason)
		}
		return ""
	})
}
