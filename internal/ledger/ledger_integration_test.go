//go:build integration

package ledger_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var ctx = context.Background()

// env is a fresh database, a registered seal key, a Ledger and one org with three demo agents.
type env struct {
	e      *storetest.Env
	signer *deed.FileSigner
	led    *ledger.Ledger
	org    ids.OrgID
	agents []store.Agent
}

func newEnv(t testing.TB, opts ...ledger.Option) *env {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 8})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(ctx, s.KeyID(), s.PublicKey()); err != nil {
		t.Fatal(err)
	}
	opts = append([]ledger.Option{ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler))}, opts...)
	en := &env{e: e, signer: s, led: ledger.New(e.DB, deed.NewSealer(s), s, opts...)}
	en.org, en.agents = en.newOrg(t, "acme")
	return en
}

func (en *env) newOrg(t testing.TB, slug string) (ids.OrgID, []store.Agent) {
	t.Helper()
	o, _, _, err := en.e.DB.CreateOrg(ctx, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "owner@" + slug + ".example.test"})
	if err != nil {
		t.Fatal(err)
	}
	var agents []store.Agent
	err = en.e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
		for _, n := range fake.AgentNames[:3] {
			a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: n, CreatedBy: "system"})
			if err != nil {
				return err
			}
			agents = append(agents, a)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID, agents
}

func (en *env) run(t testing.TB, org ids.OrgID, agents []store.Agent, seed uint64, n int, kinds ...fake.Kind) *fake.Result {
	t.Helper()
	res, err := fake.Run(ctx, en.led, org, agents, fake.Generate(seed, n, kinds, len(agents)), fake.Options{Batch: 200})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (en *env) check(t testing.TB, org ids.OrgID) *ledger.ChainCheck {
	t.Helper()
	res, err := en.led.CheckChain(ctx, org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// Every outcome seals through the one write path and the chain stays intact; each deed's sections
// match the action row they were built from.
func TestSealPaths(t *testing.T) {
	en := newEnv(t)
	res := en.run(t, en.org, en.agents, 42, 300)
	for _, k := range []fake.Kind{fake.Allow, fake.Deny, fake.Mismatch, fake.Unverifiable, fake.Failed, fake.Pending, fake.Amend} {
		if res.Counts[k] == 0 {
			t.Fatalf("seed 42 / 300 made no %s action: %v", k, res.Counts)
		}
	}
	// expired: pending_approval → expired → sealed.
	var expiredID string
	err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		id, err := en.led.CreateAction(ctx, tx, en.org, ledger.NewAction{Agent: en.agents[0].ID, Mode: "gateway", Tool: "stripe.create_refund",
			Request: json.RawMessage(`{"amount":12000}`)})
		if err != nil {
			return err
		}
		expiredID = id.String()
		for _, e := range [][2]gate.State{{gate.Received, gate.Authenticated}, {gate.Authenticated, gate.Evaluated}, {gate.Evaluated, gate.PendingApproval}, {gate.PendingApproval, gate.Expired}} {
			if err := en.led.Transition(ctx, tx, expiredID, e[0], e[1], ledger.EventData{}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.actions SET decision = 'require_approval' WHERE id = $1`, expiredID); err != nil {
			return err
		}
		_, err = en.led.SealAction(ctx, tx, expiredID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c := en.check(t, en.org)
	wantDeeds := int64(res.Deeds + 1)
	if c.Status != ledger.CheckIntact || c.Checked != wantDeeds || *c.LastSeq != wantDeeds {
		t.Fatalf("check %+v, want %d deeds intact", c, wantDeeds)
	}
	// Each deed matches its action.
	for _, id := range append(res.IDs, expiredID) {
		d, err := en.led.GetAction(ctx, en.org, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == "pending_approval" {
			if d.Deed != nil || len(d.Deeds) != 0 {
				t.Fatalf("%s: a pending action has a deed", id)
			}
			continue
		}
		if d.State != "sealed" || d.Deed == nil || len(d.Deeds) == 0 || d.Deeds[0].ID != *d.Deed {
			t.Fatalf("%s: %+v", id, d)
		}
		sd, err := en.led.GetDeed(ctx, en.org, *d.Deed)
		if err != nil {
			t.Fatal(err)
		}
		env, err := deed.ParseEnvelope(sd.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		b := env.Body
		if b.Action.ID != id || b.Action.Tool != d.Tool || b.Agent.ID != d.Agent.ID || b.Policy.Decision != *d.Policy.Decision ||
			b.Action.RequestDigest != d.RequestDigest {
			t.Fatalf("%s: deed %+v doesn't match the row", id, b)
		}
		if len(d.Deeds) == 2 { // amended: the action deed says UNVERIFIABLE, the amendment VERIFIED
			if b.Witness.Verdict != "UNVERIFIABLE" || d.Witness.Verdict != "VERIFIED" || d.Deeds[1].Kind != "amendment" {
				t.Fatalf("%s: amendment %+v / %+v", id, b.Witness, d.Witness)
			}
		} else if (b.Witness == nil) != (d.Witness == nil) || (b.Witness != nil && b.Witness.Verdict != d.Witness.Verdict) {
			t.Fatalf("%s: witness %+v vs %+v", id, b.Witness, d.Witness)
		}
		if (b.Execution == nil) != (d.Execution == nil) {
			t.Fatalf("%s: execution %+v vs %+v", id, b.Execution, d.Execution)
		}
		if b.Execution != nil && b.Execution.LatencyMS != *d.Execution.LatencyMS {
			t.Fatalf("%s: latency %d vs %d", id, b.Execution.LatencyMS, *d.Execution.LatencyMS)
		}
		if b.Kind != deed.KindAction {
			t.Fatalf("%s: kind %s", id, b.Kind)
		}
		// Events: legal edges only, ending in sealed.
		prev := ""
		for i, ev := range d.Events.Data {
			if ev.From != nil && *ev.From == ev.To {
				continue // a note (amendment)
			}
			if i > 0 && (ev.From == nil || *ev.From != prev || !gate.CanTransition(gate.State(prev), gate.State(ev.To))) {
				t.Fatalf("%s: event %d %v → %s after %s", id, ev.Seq, ev.From, ev.To, prev)
			}
			prev = ev.To
		}
		if prev != "sealed" {
			t.Fatalf("%s: last state %s", id, prev)
		}
	}
	// One admin deed.
	var adminEnv *deed.Envelope
	err = en.led.OrgTxRetry(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		adminEnv, err = en.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminLedgerPruned, Actor: deed.Ref{Type: "system", ID: "operator"},
			Target: &deed.Ref{Type: "org", ID: en.org.String()}, Reason: "test", Data: map[string]any{"actions": 0}})
		return err
	})
	if err != nil || adminEnv.Body.Admin == nil || adminEnv.Body.Admin.Action != ledger.AdminLedgerPruned {
		t.Fatalf("%+v %v", adminEnv, err)
	}
	var n int
	tx(t, en, en.org, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.admin_events WHERE deed_id = $1 AND actor_type = 'system' AND actor_id IS NULL`, adminEnv.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	if n != 1 {
		t.Fatalf("admin_events rows: %d", n)
	}
	if c := en.check(t, en.org); c.Status != ledger.CheckIntact || c.Checked != wantDeeds+1 {
		t.Fatalf("%+v", c)
	}
	// Unknown admin actions and over-long reasons are refused before anything is written.
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: "key.created", Actor: deed.Ref{Type: "system", ID: "operator"}})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "unknown admin action") {
		t.Fatalf("variant spelling accepted: %v", err)
	}
}

func tx(t testing.TB, en *env, org ids.OrgID, fn func(tx pgx.Tx)) {
	t.Helper()
	if err := en.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error { fn(tx); return nil }); err != nil {
		t.Fatal(err)
	}
}

func newAction(t testing.TB, en *env) string {
	t.Helper()
	var id string
	tx(t, en, en.org, func(tx pgx.Tx) {
		a, err := en.led.CreateAction(ctx, tx, en.org, ledger.NewAction{Agent: en.agents[0].ID, Mode: "gateway", Tool: "orders.get", Request: json.RawMessage(`{"id":1}`)})
		if err != nil {
			t.Fatal(err)
		}
		id = a.String()
	})
	return id
}

func eventCount(t testing.TB, en *env, id string) int {
	var n int
	tx(t, en, en.org, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.action_events WHERE action_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	return n
}

// Illegal edges write nothing; skipping the witness (executed → sealed) is refused.
func TestIllegalTransition(t *testing.T) {
	en := newEnv(t)
	id := newAction(t, en)
	before := eventCount(t, en, id)
	err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		return en.led.Transition(ctx, tx, id, gate.Received, gate.Evaluated, ledger.EventData{})
	})
	if !errors.Is(err, ledger.ErrIllegalTransition) {
		t.Fatalf("received → evaluated: %v", err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		return en.led.Transition(ctx, tx, id, gate.Executed, gate.Sealed, ledger.EventData{})
	})
	if !errors.Is(err, ledger.ErrIllegalTransition) {
		t.Fatalf("executed → sealed: %v", err)
	}
	// Moving from a state the action isn't in: a conflict, not a transition.
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		return en.led.Transition(ctx, tx, id, gate.Authenticated, gate.Evaluated, ledger.EventData{})
	})
	if !errors.Is(err, ledger.ErrStateConflict) {
		t.Fatalf("wrong from: %v", err)
	}
	// Sealing an action that isn't final.
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := en.led.SealAction(ctx, tx, id)
		return err
	})
	if !errors.Is(err, ledger.ErrNotSealable) {
		t.Fatalf("seal a received action: %v", err)
	}
	if got := eventCount(t, en, id); got != before {
		t.Fatalf("events %d → %d after refusals", before, got)
	}
	if h, _ := en.led.HeadSeq(ctx, en.org); h != 0 {
		t.Fatalf("a refused seal left deed %d", h)
	}
}

// Two transactions race for the same edge: one wins, the other gets ErrStateConflict.
func TestConcurrentTransition(t *testing.T) {
	en := newEnv(t)
	id := newAction(t, en)
	hold := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(1)
	go func() {
		defer wg.Done()
		err1 = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			if err := en.led.Transition(ctx, tx, id, gate.Received, gate.Authenticated, ledger.EventData{}); err != nil {
				return err
			}
			close(hold)
			<-release
			return nil
		})
	}()
	<-hold
	wg.Add(1)
	go func() {
		defer wg.Done()
		err2 = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			return en.led.Transition(ctx, tx, id, gate.Received, gate.Authenticated, ledger.EventData{})
		})
	}()
	time.Sleep(200 * time.Millisecond) // the second one is now waiting on the row lock
	close(release)
	wg.Wait()
	if err1 != nil || !errors.Is(err2, ledger.ErrStateConflict) {
		t.Fatalf("first %v, second %v", err1, err2)
	}
	if n := eventCount(t, en, id); n != 2 {
		t.Fatalf("%d events, want 2", n)
	}
}

// NOTIFY is delivered at COMMIT, never after ROLLBACK, and carries ids only.
func TestNotify(t *testing.T) {
	en := newEnv(t)
	l := en.e.App(t)
	if _, err := l.Exec(ctx, "LISTEN "+ledger.Channel); err != nil {
		t.Fatal(err)
	}
	wait := func(d time.Duration) []ledger.Event {
		var out []ledger.Event
		deadline := time.Now().Add(d)
		for {
			wctx, cancel := context.WithDeadline(ctx, deadline)
			n, err := l.WaitForNotification(wctx)
			cancel()
			if err != nil {
				return out
			}
			var ev ledger.Event
			if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
				t.Fatalf("payload %s: %v", n.Payload, err)
			}
			if len(n.Payload) > ledger.MaxNotifyPayload || strings.Contains(n.Payload, "request") || strings.Contains(n.Payload, "result") {
				t.Fatalf("payload %s", n.Payload)
			}
			out = append(out, ev)
		}
	}
	// Rolled back: nothing.
	err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := en.led.CreateAction(ctx, tx, en.org, ledger.NewAction{Agent: en.agents[0].ID, Mode: "gateway", Tool: "orders.get"}); err != nil {
			return err
		}
		return errors.New("roll back")
	})
	if err == nil {
		t.Fatal("expected the rollback error")
	}
	if got := wait(400 * time.Millisecond); len(got) != 0 {
		t.Fatalf("events after ROLLBACK: %+v", got)
	}
	// Committed: action.created … deed.sealed, in order.
	res := en.run(t, en.org, en.agents, 3, 1, fake.Allow)
	got := wait(time.Second)
	var types []string
	for _, ev := range got {
		types = append(types, ev.Type)
		if ev.Org != en.org.String() || !strings.HasPrefix(ev.Event, "evt_") || ev.V != 1 {
			t.Fatalf("%+v", ev)
		}
	}
	want := []string{"action.created", "action.decided", "action.executed", "action.witnessed", "deed.sealed"}
	if !slices.Equal(types, want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	last := got[len(got)-1]
	if last.Seq == nil || *last.Seq != res.LastSeq || last.Deed == nil || last.ID != *last.Deed || *last.Action != res.IDs[0] {
		t.Fatalf("deed.sealed %+v", last)
	}
}

// listAll pages through ListActions.
func listAll(t testing.TB, en *env, org ids.OrgID, v url.Values, limit int) []ledger.Action {
	t.Helper()
	v.Set("limit", fmt.Sprint(limit))
	var out []ledger.Action
	for range 10_000 {
		f, p, err := ledger.ParseActionQuery(v)
		if err != nil {
			t.Fatal(err)
		}
		l, err := en.led.ListActions(ctx, org, f, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l.Data...)
		if !l.HasMore {
			return out
		}
		v.Set("cursor", *l.NextCursor)
	}
	t.Fatal("too many pages")
	return nil
}

// The filter matrix over 2,000 actions equals an in-memory reference filter.
func TestFilterMatrix(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 11, 2000)
	all := listAll(t, en, en.org, url.Values{}, 100)
	if len(all) != 2000 {
		t.Fatalf("%d actions", len(all))
	}
	mid := time.Time(all[1000].CreatedAt)
	ref := func(keep func(a ledger.Action) bool) []string {
		var out []string
		for _, a := range all {
			if keep(a) {
				out = append(out, a.ID)
			}
		}
		return out
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	cases := []struct {
		q    string
		keep func(a ledger.Action) bool
	}{
		{"agent=" + en.agents[1].ID.String(), func(a ledger.Action) bool { return a.Agent.ID == en.agents[1].ID.String() }},
		{"state=pending_approval", func(a ledger.Action) bool { return a.State == "pending_approval" }},
		{"verdict=MISMATCH", func(a ledger.Action) bool { return a.Witness != nil && a.Witness.Verdict == "MISMATCH" }},
		{"verdict=VERIFIED,UNVERIFIABLE&level=L2", func(a ledger.Action) bool {
			return a.Witness != nil && a.Witness.Level == "L2" && (a.Witness.Verdict == "VERIFIED" || a.Witness.Verdict == "UNVERIFIABLE")
		}},
		{"decision=deny", func(a ledger.Action) bool { return deref(a.Policy.Decision) == "deny" }},
		{"tool=stripe.*", func(a ledger.Action) bool { return strings.HasPrefix(a.Tool, "stripe.") }},
		{"tool=*.execute&agent=" + en.agents[0].ID.String(), func(a ledger.Action) bool {
			return strings.HasSuffix(a.Tool, ".execute") && a.Agent.ID == en.agents[0].ID.String()
		}},
		{"q=refund", func(a ledger.Action) bool { return strings.Contains(a.Tool, "refund") }},
		{"q=UPSTREAM", func(a ledger.Action) bool {
			return a.Execution != nil && strings.Contains(strings.ToUpper(deref(a.Execution.ErrorCode)), "UPSTREAM")
		}},
		{"created_at[lt]=" + url.QueryEscape(mid.Format(time.RFC3339Nano)), func(a ledger.Action) bool { return time.Time(a.CreatedAt).Before(mid) }},
		{"created_at[gte]=" + url.QueryEscape(mid.Format(time.RFC3339Nano)) + "&verdict=VERIFIED", func(a ledger.Action) bool {
			return !time.Time(a.CreatedAt).Before(mid) && a.Witness != nil && a.Witness.Verdict == "VERIFIED"
		}},
		{"tool=a_b", func(a ledger.Action) bool { return false }}, // _ is a literal, not a wildcard
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		var got []string
		for _, a := range listAll(t, en, en.org, v, 37) {
			got = append(got, a.ID)
		}
		want := ref(c.keep)
		if !slices.Equal(got, want) {
			t.Errorf("%s: %d ids, want %d", c.q, len(got), len(want))
		}
		f, _, _ := ledger.ParseActionQuery(v)
		if n, err := en.led.CountActions(ctx, en.org, f); err != nil || n != int64(len(want)) {
			t.Errorf("%s: count %d %v, want %d", c.q, n, err, len(want))
		}
	}
	// A cursor reused with other filters is refused.
	f, p, _ := ledger.ParseActionQuery(url.Values{"verdict": {"VERIFIED"}, "limit": {"5"}})
	l, err := en.led.ListActions(ctx, en.org, f, p)
	if err != nil {
		t.Fatal(err)
	}
	f2, p2, _ := ledger.ParseActionQuery(url.Values{"verdict": {"MISMATCH"}, "cursor": {*l.NextCursor}})
	var pe *ledger.ParamError
	if _, err := en.led.ListActions(ctx, en.org, f2, p2); !errors.As(err, &pe) || pe.Param != "cursor" {
		t.Fatalf("stale cursor: %v", err)
	}
}

// S26: shadow_differs=true lists the actions whose shadow policy decided, and decided differently
// from the enforced decision (a NULL decision counts as different), over pages, with the count,
// and only in the caller's org.
func TestShadowDiffersFilter(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 7, 300)
	orgB, agentsB := en.newOrg(t, "beta")
	en.run(t, orgB, agentsB, 8, 30)
	// A third of the rows shadow-differ, a third agree, a third have no shadow decision.
	shadow := `WITH n AS (SELECT id, row_number() OVER (ORDER BY id) AS i FROM deedglass.actions)
	  UPDATE deedglass.actions AS a SET shadow_decision = CASE n.i % 3
	      WHEN 0 THEN CASE WHEN a.decision = 'allow' THEN 'deny' ELSE 'allow' END
	      WHEN 1 THEN a.decision END
	    FROM n WHERE n.id = a.id`
	for _, o := range []ids.OrgID{en.org, orgB} {
		if err := en.e.DB.OrgTx(ctx, o, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, shadow); return err }); err != nil {
			t.Fatal(err)
		}
	}
	// One row with a shadow decision and no enforced one.
	if err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET shadow_decision = 'deny'
		    WHERE id = (SELECT id FROM deedglass.actions WHERE decision IS NULL ORDER BY id LIMIT 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var want []string
	var nulls int
	err := en.e.DB.OrgReadTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, decision, shadow_decision FROM deedglass.actions ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var d, s *string
			if err := rows.Scan(&id, &d, &s); err != nil {
				return err
			}
			if s != nil && (d == nil || *d != *s) {
				want = append(want, id)
				if d == nil {
					nulls++
				}
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 90 {
		t.Fatalf("only %d rows differ", len(want))
	}
	var got []string
	for _, a := range listAll(t, en, en.org, url.Values{"shadow_differs": {"true"}}, 37) {
		got = append(got, a.ID)
		if a.Decision != nil && (a.Decision.Shadow == nil || a.Decision.Shadow.Outcome == a.Decision.Outcome) {
			t.Errorf("%s: shadow %+v, decision %s", a.ID, a.Decision.Shadow, a.Decision.Outcome)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("shadow_differs: %d ids, want %d (%d without a decision)", len(got), len(want), nulls)
	}
	f, _, _ := ledger.ParseActionQuery(url.Values{"shadow_differs": {"true"}})
	if n, err := en.led.CountActions(ctx, en.org, f); err != nil || n != int64(len(want)) {
		t.Fatalf("count %d %v, want %d", n, err, len(want))
	}
	// With another filter, and a cursor from the plain list is refused.
	f2, p2, _ := ledger.ParseActionQuery(url.Values{"shadow_differs": {"true"}, "decision": {"deny"}})
	l, err := en.led.ListActions(ctx, en.org, f2, p2)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range l.Data {
		if a.Policy.Decision == nil || *a.Policy.Decision != "deny" || a.Decision.Shadow == nil || a.Decision.Shadow.Outcome == "deny" {
			t.Fatalf("%s: %+v", a.ID, a.Decision)
		}
	}
	fp, pp, _ := ledger.ParseActionQuery(url.Values{"limit": {"5"}})
	lp, err := en.led.ListActions(ctx, en.org, fp, pp)
	if err != nil {
		t.Fatal(err)
	}
	f3, p3, _ := ledger.ParseActionQuery(url.Values{"shadow_differs": {"true"}, "cursor": {*lp.NextCursor}})
	var pe *ledger.ParamError
	if _, err := en.led.ListActions(ctx, en.org, f3, p3); !errors.As(err, &pe) || pe.Param != "cursor" {
		t.Fatalf("cursor reuse: %v", err)
	}
}

// Paging through 1,000 actions while 500 more arrive: no duplicate, nothing skipped.
func TestPaginationUnderConcurrentInserts(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 5, 1000)
	var original []string
	for _, a := range listAll(t, en, en.org, url.Values{}, 100) {
		original = append(original, a.ID)
	}
	done := make(chan error, 1)
	go func() {
		_, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(6, 500, nil, 3), fake.Options{Batch: 5})
		done <- err
	}()
	seen := map[string]int{}
	for _, a := range listAll(t, en, en.org, url.Values{}, 37) {
		seen[a.ID]++
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("%s seen %d times", id, n)
		}
	}
	for _, id := range original {
		if seen[id] != 1 {
			t.Fatalf("%s skipped", id)
		}
	}
}

// Another org's ids are not found, and its rows are invisible.
func TestRLS(t *testing.T) {
	en := newEnv(t)
	res := en.run(t, en.org, en.agents, 9, 20, fake.Allow)
	orgB, agentsB := en.newOrg(t, "beta")
	en.run(t, orgB, agentsB, 9, 5, fake.Allow)
	act := res.IDs[0]
	d, err := en.led.GetAction(ctx, en.org, act)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := en.led.GetAction(ctx, orgB, act); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("org B read org A's action: %v", err)
	}
	if _, err := en.led.GetDeed(ctx, orgB, *d.Deed); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("org B read org A's deed: %v", err)
	}
	if _, err := en.led.GetDeed(ctx, orgB, "1"); err != nil {
		t.Fatalf("org B's own deed 1: %v", err)
	}
	var n int
	tx(t, en, orgB, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM deedglass.actions WHERE id = $1) + (SELECT count(*) FROM deedglass.deeds WHERE id = $2)
		    + (SELECT count(*) FROM deedglass.action_events WHERE action_id = $1)`, act, *d.Deed).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	if n != 0 {
		t.Fatalf("org B's transaction sees %d of org A's rows", n)
	}
	l, err := en.led.ListActions(ctx, orgB, ledger.ActionFilter{}, ledger.Page{Limit: 100})
	if err != nil || len(l.Data) != 5 {
		t.Fatalf("org B lists %d actions: %v", len(l.Data), err)
	}
	if c := en.check(t, orgB); c.Status != ledger.CheckIntact || c.Checked != 5 {
		t.Fatalf("org B chain %+v", c)
	}
}

// superuser runs statements as the cluster superuser, which only a throwaway test cluster has.
func superuser(t testing.TB, en *env, stmts ...string) {
	t.Helper()
	c := en.e.Admin(t)
	for _, s := range stmts {
		if _, err := c.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// Someone with database superuser rights rewrites a deed. In production the append-only trigger
// refuses this for every Deedglass role; here we disable it as a superuser on a throwaway cluster,
// which is exactly the attack the chain check (and every external verify of an export) exists to
// catch.
func TestChainBreakRewrite(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 21, 40, fake.Allow)
	var seq int64
	var envBytes []byte
	tx(t, en, en.org, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `SELECT seq, envelope FROM deedglass.deeds WHERE org_id = $1 AND seq = 12`, en.org).Scan(&seq, &envBytes); err != nil {
			t.Fatal(err)
		}
	})
	i := bytes.Index(envBytes, []byte(`"latency_ms":`))
	if i < 0 {
		t.Fatal("deed 12 has no latency_ms")
	}
	j := i + len(`"latency_ms":`)
	tampered := append([]byte{}, envBytes...)
	tampered[j] = "1234567891"[tampered[j]-'0'] // one digit changes
	superuser(t, en,
		"ALTER TABLE deedglass.deeds DISABLE TRIGGER deeds_append_only",
		"ALTER TABLE deedglass.deeds DISABLE TRIGGER deeds_append_only_stmt")
	c := en.e.Admin(t)
	if _, err := c.Exec(ctx, `UPDATE deedglass.deeds SET envelope = $1 WHERE org_id = $2 AND seq = 12`, tampered, en.org); err != nil {
		t.Fatal(err)
	}
	superuser(t, en,
		"ALTER TABLE deedglass.deeds ENABLE TRIGGER deeds_append_only",
		"ALTER TABLE deedglass.deeds ENABLE TRIGGER deeds_append_only_stmt")
	res := en.check(t, en.org)
	if res.Status != ledger.CheckBroken || res.Break == nil || *res.Break.Seq != 12 || res.Break.Code != "body_hash_mismatch" ||
		res.Break.Message != "Deed 12 was changed after it was sealed." || res.Checked != 11 {
		t.Fatalf("%+v %+v", res, res.Break)
	}
	// A range after the break is anchored on deed 12's stored body_hash and reads intact.
	after, err := en.led.CheckChain(ctx, en.org, ledger.Range{FromSeq: 13}, ledger.CheckOptions{})
	if err != nil || after.Status != ledger.CheckIntact || after.Anchor.Kind != "deed" || after.Anchor.Seq != 12 {
		t.Fatalf("%+v %v", after, err)
	}
}

// A deleted deed shows as a gap.
func TestChainBreakDelete(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 22, 20, fake.Deny)
	c := en.e.Admin(t)
	// replica mode skips the append-only triggers and the foreign keys that point at the deed.
	if _, err := c.Exec(ctx, "SET session_replication_role = replica"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `DELETE FROM deedglass.deeds WHERE org_id = $1 AND seq = 7`, en.org); err != nil {
		t.Fatal(err)
	}
	res := en.check(t, en.org)
	if res.Status != ledger.CheckBroken || res.Break.Code != "chain_seq_gap" || *res.Break.Seq != 8 || res.Checked != 6 {
		t.Fatalf("%+v %+v", res, res.Break)
	}
}

// The budget stops a check with next_from_seq; resuming there finishes it.
func TestCheckIncomplete(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 23, 30, fake.Allow)
	res, err := en.led.CheckChain(ctx, en.org, ledger.Range{}, ledger.CheckOptions{Chunk: 10, MaxDeeds: 10})
	if err != nil || res.Status != ledger.CheckIncomplete || res.Checked != 10 || *res.NextFromSeq != 11 {
		t.Fatalf("%+v %v", res, err)
	}
	rest, err := en.led.CheckChain(ctx, en.org, ledger.Range{FromSeq: 11}, ledger.CheckOptions{})
	if err != nil || rest.Status != ledger.CheckIntact || rest.Checked != 20 || rest.Head.Seq != 30 {
		t.Fatalf("%+v %v", rest, err)
	}
	if _, err := en.led.CheckChain(ctx, en.org, ledger.Range{FromSeq: 31}, ledger.CheckOptions{}); !errors.Is(err, ledger.ErrEmptyRange) {
		t.Fatalf("empty range: %v", err)
	}
	empty, _ := en.newOrg(t, "empty")
	if c := en.check(t, empty); c.Status != ledger.CheckIntact || c.Checked != 0 || c.Head != nil {
		t.Fatalf("empty org %+v", c)
	}
}

// verifyBundle checks a bundle the way `deedglass verify` does, with the server's keys pinned.
func verifyBundle(t testing.TB, en *env, b []byte) (*deed.Report, *archive.Manifest) {
	t.Helper()
	rows, err := en.e.DB.SealKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("http://127.0.0.1:8080", recs)
	if err != nil {
		t.Fatal(err)
	}
	r, err := archive.Open(bytes.NewReader(b), archive.Limits{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	v := deed.NewVerifier(deed.StreamOptions{Keys: ks, KeysPinned: true})
	if err := v.Stream(ctx, r.Deeds(), "deeds.jsonl"); err != nil {
		t.Fatal(err)
	}
	m, bs, err := r.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	in := v.Input()
	if err := m.Check(archive.Observed{Count: in.Count, FirstSeq: in.FirstSeq, LastSeq: in.LastSeq, From: in.From, To: in.To,
		FirstPrevHash: in.FirstPrevHash, Kinds: in.Kinds, Head: archive.ChainHead{Seq: in.HeadSeq, DeedID: in.HeadDeedID, BodyHash: in.HeadBodyHash}}); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if bs == nil {
		t.Fatal("manifest not sealed")
	}
	if err := deed.VerifyBundleSeal(bs, r.ManifestBytes(), ks, mustTime(t, m.CreatedAt)); err != nil {
		t.Fatalf("manifest seal: %v", err)
	}
	return v.Report(), m
}

func mustTime(t testing.TB, s string) time.Time {
	tm, err := deed.ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// Export → verify, byte-identical twice with the same created_at; ranges and an empty range.
func TestExportThenVerify(t *testing.T) {
	// A fixed export clock (two exports must be byte-identical), after the seal key's activation:
	// a calendar date here made the bundle's seal predate its key once that date passed (S25).
	pinned := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	en := newEnv(t, ledger.WithClock(func() time.Time { return pinned }))
	res := en.run(t, en.org, en.agents, 42, 1204, fake.Allow, fake.Deny, fake.Mismatch, fake.Unverifiable, fake.Failed)
	if res.Deeds != 1204 {
		t.Fatalf("%d deeds", res.Deeds)
	}
	var a, b bytes.Buffer
	if _, err := en.led.Export(ctx, en.org, ledger.Range{}, &a); err != nil {
		t.Fatal(err)
	}
	if _, err := en.led.Export(ctx, en.org, ledger.Range{}, &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("two exports of the same range differ (%x vs %x)", sha256.Sum256(a.Bytes()), sha256.Sum256(b.Bytes()))
	}
	rep, m := verifyBundle(t, en, a.Bytes())
	if rep.Result != deed.ResultValid || rep.Intact != 1204 || m.Range.Count != 1204 || m.Chain.Anchor.Kind != "genesis" {
		t.Fatalf("%+v %+v", rep, m.Range)
	}
	// A middle range is anchored on the deed before it.
	var mid bytes.Buffer
	m2, err := en.led.Export(ctx, en.org, ledger.Range{FromSeq: 100, ToSeq: 199}, &mid)
	if err != nil || m2.Range.Count != 100 || m2.Chain.Anchor.Kind != "deed" || m2.Chain.Anchor.Seq != 99 {
		t.Fatalf("%+v %v", m2, err)
	}
	ar, err := archive.Open(bytes.NewReader(mid.Bytes()), archive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var lines int
	sc := bufioScanner(ar.Deeds())
	for sc.Scan() {
		lines++
	}
	if lines != 100 {
		t.Fatalf("%d lines", lines)
	}
	// JSONL export: the stored bytes, one per line.
	var jl bytes.Buffer
	first, last, n, err := en.led.ExportJSONL(ctx, en.org, ledger.Range{ToSeq: 10}, &jl)
	if err != nil || first != 1 || last != 10 || n != 10 || bytes.Count(jl.Bytes(), []byte("\n")) != 10 {
		t.Fatalf("%d %d %d %v", first, last, n, err)
	}
	// Empty range: an error that says where the chain ends.
	var empty *ledger.EmptyRangeError
	if _, err := en.led.Export(ctx, en.org, ledger.Range{FromSeq: 5000, ToSeq: 6000}, io.Discard); !errors.As(err, &empty) || empty.Head != 1204 {
		t.Fatalf("empty range: %v", err)
	}
}

func bufioScanner(r io.Reader) *lineScanner { return &lineScanner{r: r} }

type lineScanner struct {
	r   io.Reader
	buf []byte
	err error
}

func (s *lineScanner) Scan() bool {
	if s.buf == nil {
		s.buf, s.err = io.ReadAll(s.r)
	}
	i := bytes.IndexByte(s.buf, '\n')
	if i < 0 {
		return false
	}
	s.buf = s.buf[i+1:]
	return true
}

// cancelAfter cancels its context once n bytes went through: a client that hangs up.
type cancelAfter struct {
	w      io.Writer
	n      int
	cancel context.CancelFunc
}

func (c *cancelAfter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n -= n
	if c.n <= 0 {
		c.cancel()
	}
	return n, err
}

// An export cut off mid-stream rolls back, and its partial file fails verification.
func TestExportAbort(t *testing.T) {
	en := newEnv(t)
	en.run(t, en.org, en.agents, 31, 2000, fake.Allow, fake.Deny)
	var full bytes.Buffer
	if _, err := en.led.Export(ctx, en.org, ledger.Range{}, &full); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var part bytes.Buffer
	_, err := en.led.Export(cctx, en.org, ledger.Range{}, &cancelAfter{w: &part, n: full.Len() / 10, cancel: cancel})
	if err == nil {
		t.Fatal("the export finished after its context was cancelled")
	}
	if part.Len() >= full.Len() {
		t.Fatalf("partial %d of %d bytes", part.Len(), full.Len())
	}
	r, err := archive.Open(bytes.NewReader(part.Bytes()), archive.Limits{})
	if err == nil {
		_, _ = io.Copy(io.Discard, r.Deeds())
		_, _, err = r.Finish()
	}
	if deed.CodeOf(err) != deed.CodeBundleInvalid {
		t.Fatalf("partial bundle: %v (code %q)", err, deed.CodeOf(err))
	}
}

// Prune removes payloads of sealed actions only, seals one ledger.pruned deed, and every deed
// still verifies.
func TestPruneThenVerify(t *testing.T) {
	en := newEnv(t)
	res := en.run(t, en.org, en.agents, 42, 300)
	var deedsBefore [32]byte
	digest := func() [32]byte {
		var all []byte
		tx(t, en, en.org, func(tx pgx.Tx) {
			rows, err := tx.Query(ctx, `SELECT envelope FROM deedglass.deeds WHERE org_id = $1 ORDER BY seq`, en.org)
			if err != nil {
				t.Fatal(err)
			}
			envs, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range envs {
				all = append(all, e...)
			}
		})
		return sha256.Sum256(all)
	}
	deedsBefore = digest()
	events := func() int {
		var n int
		tx(t, en, en.org, func(tx pgx.Tx) {
			_ = tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.action_events`).Scan(&n)
		})
		return n
	}
	evBefore := events()
	// Outside dev, a fresh cutoff is refused.
	prodLed := ledger.New(en.e.DB, deed.NewSealer(en.signer), en.signer, ledger.WithDev(false))
	if _, err := prodLed.Prune(ctx, en.org, time.Now(), ledger.PruneOptions{}); err == nil {
		t.Fatal("a 0 s cutoff was accepted outside dev")
	}
	dry, err := en.led.Prune(ctx, en.org, time.Now().Add(time.Second), ledger.PruneOptions{DryRun: true})
	if err != nil || dry.Actions != int64(res.Actions-res.Counts[fake.Pending]) || dry.Deed != nil {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if digest() != deedsBefore {
		t.Fatal("a dry run changed deeds")
	}
	pr, err := en.led.Prune(ctx, en.org, time.Now().Add(time.Second), ledger.PruneOptions{RetentionDays: 90})
	if err != nil || pr.Actions != dry.Actions || pr.Deed == nil || *pr.Seq != res.LastSeq+1 || pr.Bytes <= 0 {
		t.Fatalf("prune %+v %v", pr, err)
	}
	// Deeds before the new one are untouched; events too.
	tx(t, en, en.org, func(tx pgx.Tx) {
		var n, nulls, pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'admin'),
		    (SELECT count(*) FROM deedglass.actions WHERE state = 'sealed' AND request IS NULL AND result IS NULL AND witness_detail IS NULL AND payload_pruned_at IS NOT NULL),
		    (SELECT count(*) FROM deedglass.actions WHERE state <> 'sealed' AND request IS NOT NULL AND payload_pruned_at IS NULL)
		  FROM deedglass.deeds`).Scan(&n, &nulls, &pending); err != nil {
			t.Fatal(err)
		}
		if n != 1 || int64(nulls) != pr.Actions || pending != res.Counts[fake.Pending] {
			t.Fatalf("admin deeds %d, pruned %d, pending kept %d", n, nulls, pending)
		}
	})
	if events() != evBefore {
		t.Fatal("prune changed action_events")
	}
	d, err := en.led.GetAction(ctx, en.org, res.IDs[len(res.IDs)-1])
	if err != nil {
		t.Fatal(err)
	}
	if d.State == "sealed" && (string(d.Request) != "null" || d.PayloadPrunedAt == nil || d.RequestDigest == "") {
		t.Fatalf("pruned action: request %s pruned %v", d.Request, d.PayloadPrunedAt)
	}
	if c := en.check(t, en.org); c.Status != ledger.CheckIntact || c.Checked != res.LastSeq+1 {
		t.Fatalf("%+v", c)
	}
	var b bytes.Buffer
	if _, err := en.led.Export(ctx, en.org, ledger.Range{}, &b); err != nil {
		t.Fatal(err)
	}
	if rep, _ := verifyBundle(t, en, b.Bytes()); rep.Result != deed.ResultValid || rep.Kinds["admin"] != 1 {
		t.Fatalf("%+v", rep)
	}
	// Running again finds nothing and seals nothing.
	again, err := en.led.Prune(ctx, en.org, time.Now().Add(time.Second), ledger.PruneOptions{})
	if err != nil || again.Actions != 0 || again.Deed != nil {
		t.Fatalf("second prune %+v %v", again, err)
	}
	// One prune per org at a time.
	errc := make(chan error, 1)
	err = en.e.DB.WithLock(ctx, "deedglass/prune/"+en.org.String(), func(ctx context.Context) error {
		_, err := en.led.Prune(ctx, en.org, time.Now(), ledger.PruneOptions{})
		errc <- err
		return nil
	})
	if err != nil || !errors.Is(<-errc, ledger.ErrPruneRunning) {
		t.Fatalf("concurrent prune: %v", err)
	}
}

// TestAdvance (S13): several edges as one statement write one event per edge with gapless seqs,
// every state's timestamp, and the merged patch; a skipped edge is refused here and, when it gets
// past the Go check, by the database guard (DG004); a wrong from is a conflict.
func TestAdvance(t *testing.T) {
	en := newEnv(t)
	id := newAction(t, en)
	deadline := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	dec := "allow"
	err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		return en.led.Advance(ctx, tx, id, gate.Received,
			ledger.Step{To: gate.Authenticated},
			ledger.Step{To: gate.Evaluated, Patch: &ledger.Patch{Decision: &dec}},
			ledger.Step{To: gate.Executing, Patch: &ledger.Patch{ExecutingDeadline: &deadline}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var state, decision string
	var dl time.Time
	var seqs []int
	var nulls int
	_ = en.e.DB.OrgReadTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT state, decision, executing_deadline,
		    (authenticated_at IS NULL)::int + (evaluated_at IS NULL)::int + (executing_at IS NULL)::int + (hops IS NOT NULL)::int
		    FROM deedglass.actions WHERE id = $1`, id).Scan(&state, &decision, &dl, &nulls); err != nil {
			return err
		}
		rows, _ := tx.Query(ctx, `SELECT seq FROM deedglass.action_events WHERE action_id = $1 ORDER BY seq`, id)
		seqs, _ = pgx.CollectRows(rows, pgx.RowTo[int])
		return nil
	})
	if state != "executing" || decision != "allow" || !dl.Equal(deadline) || nulls != 0 || len(seqs) != 4 || seqs[3] != 4 {
		t.Fatalf("%s %s %v nulls=%d seqs=%v", state, decision, dl, nulls, seqs)
	}
	for _, c := range []struct {
		from  gate.State
		steps []ledger.Step
		want  error
	}{
		{gate.Executing, []ledger.Step{{To: gate.Witnessing}, {To: gate.Verified}}, ledger.ErrIllegalTransition},
		{gate.Executing, []ledger.Step{{To: gate.Executed}, {To: gate.Sealed}}, ledger.ErrIllegalTransition},
		{gate.Received, []ledger.Step{{To: gate.Authenticated}, {To: gate.Evaluated}}, ledger.ErrStateConflict},
		{gate.Executing, []ledger.Step{{To: gate.Executed, Patch: &ledger.Patch{Decision: &dec}}, {To: gate.Witnessing}}, ledger.ErrIllegalTransition},
	} {
		err := en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
			return en.led.Advance(ctx, tx, id, c.from, c.steps...)
		})
		if !errors.Is(err, c.want) {
			t.Errorf("%s %v: %v, want %v", c.from, c.steps, err, c.want)
		}
	}
	// The database guard checks every hop on its own.
	_, err = en.e.App(t).Exec(ctx, `SELECT 1`) // a plain app connection works
	if err != nil {
		t.Fatal(err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET state = 'witnessing', hops = ARRAY['verified'], witnessing_at = now(), verified_at = now() WHERE id = $1`, id)
		return err
	})
	if store.SQLState(err) != store.CodeIllegalTransition {
		t.Fatalf("a skipped hop: %v, want DG004", err)
	}
	err = en.e.DB.OrgTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET state = 'witnessing', hops = ARRAY['executed'], executed_at = now(), witnessing_at = now(),
		    execution_status = 'succeeded' WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatalf("a legal hop list: %v", err)
	}
}
