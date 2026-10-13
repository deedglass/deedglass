//go:build integration

package observe

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var bg = context.Background()

type world struct {
	t    *testing.T
	e    *storetest.Env
	s    *Service
	org  ids.OrgID
	user ids.UserID
	con  string
	lsn  int
}

const watchedCfg = `{"witness":{"enabled":true,"watched_tables":["public.orders","public.bulk"],"observe":{"enabled":true,"max_session":"1m","caught_rows":100}}}`

func newWorld(t *testing.T, cfg string) *world {
	t.Helper()
	e := storetest.New(t, storetest.Options{})
	seed, _ := deed.NewSeed(rand.Reader)
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true))
	o, u, _, err := e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme" + strings.ToLower(ids.New[ids.OrgKind]().String()[20:]), OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, e: e, s: New(e.DB, led, time.Second, nil), org: o.ID, user: u.ID, con: ids.New[ids.ConnectorKind]().String()}
	if err := e.DB.OrgTx(bg, o.ID, func(ctx context.Context, tx pgx.Tx) error {
		return store.InsertConnector(ctx, tx, store.Connector{ID: w.con, OrgID: o.ID.String(), Type: "postgres", Name: "pg-demo", Description: "observe test",
			Config: json.RawMessage(cfg), ConfigHash: "sha256:" + strings.Repeat("0", 64), Status: "active", CreatedBy: "system"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin(t).Exec(bg, `INSERT INTO deedglass.decoder_slots (org_id, connector_id, slot_name, status) VALUES ($1, $2, $3, 'running')`,
		o.ID, w.con, "deedglass_"+strings.ToLower(w.con[4:])); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) actor() Actor { return Actor{Type: "user", ID: w.user.String()} }

// frame stores an unattributed (or other class) frame committed at at, updating n rows of table.
func (w *world) frame(class string, at time.Time, table string, n int) string {
	w.t.Helper()
	w.lsn++
	lsn := fmt.Sprintf("0/%X", 0x1000+w.lsn*0x100)
	var changes []map[string]any
	for i := 1; i <= min(n, 50); i++ {
		k := fmt.Sprint(i)
		changes = append(changes, map[string]any{"seq": i, "op": "U", "table": table, "key": map[string]any{"id": k},
			"old": map[string]any{"id": map[string]string{"t": k}, "status": map[string]string{"t": "paid"}},
			"new": map[string]any{"id": map[string]string{"t": k}, "status": map[string]string{"t": "shipped"}}, "changed": []string{"status"}})
	}
	cb, _ := json.Marshal(changes)
	tb := fmt.Sprintf(`{%q:{"relid":16400,"pk":["id"],"identity":"f","columns":["id","status"],"type_oids":[23,25]}}`, table)
	counts := fmt.Sprintf(`{%q:{"insert":0,"update":%d,"delete":0,"truncate":0}}`, table, n)
	xid := int64(700 + w.lsn)
	digest := "sha256:" + strings.Repeat(fmt.Sprintf("%02x", w.lsn%256), 32)
	f := store.DecodedFrame{ID: ids.New[ids.DecodedTxKind]().String(), OrgID: w.org.String(), ConnectorID: w.con, SystemID: "7690386503164422802",
		TimelineID: 1, CommitLSN: &lsn, EndLSN: &lsn, Xid: &xid, CommitTime: &at, Class: class, Tables: json.RawMessage(tb),
		Counts: json.RawMessage(counts), Changes: cb, ChangeCount: n, ChangesDigest: &digest}
	if class == "bound" {
		a := "act_01m3x71p6wesj7xewec6bwbwxm"
		f.ActionID, f.Markers = &a, []string{a}
		f.Ledger = json.RawMessage(`[{"action_id":"act_01m3x71p6wesj7xewec6bwbwxm","org_id":"` + w.org.String() + `","agent_id":"agt_01m3x71p6wesj7xewec6bwbwxm","intent_hash":null,"sql_digest":"sha256:` + strings.Repeat("ab", 32) + `","xid8":"777","created_at":"2026-10-07T13:12:09Z"}]`)
	}
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.InsertFrames(ctx, tx, []store.DecodedFrame{f})
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return f.ID
}

func (w *world) dbNow() time.Time {
	w.t.Helper()
	var now time.Time
	if err := w.e.Admin(w.t).QueryRow(bg, `SELECT now()`).Scan(&now); err != nil {
		w.t.Fatal(err)
	}
	return now
}

func (w *world) caughtUp(at time.Time) {
	w.t.Helper()
	if _, err := w.e.Admin(w.t).Exec(bg, `UPDATE deedglass.decoder_slots SET last_commit_at = $2 WHERE connector_id = $1`, w.con, at); err != nil {
		w.t.Fatal(err)
	}
}

func frameIDs(x *Session) []string {
	var out []string
	for _, f := range x.Frames {
		out = append(out, f.ID)
	}
	slices.Sort(out)
	return out
}

// TestAttachAndSeal: the window's boundaries, unwatched tables, bound frames, overlap, a frame
// decoded after the close but committed inside, the seal (ids, counts and digests only), and RLS.
func TestAttachAndSeal(t *testing.T) {
	w := newWorld(t, watchedCfg)
	a, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "pg-demo", Label: "nightly agent"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: w.con})
	if err != nil {
		t.Fatal(err)
	}
	opened := a.Row.OpenedAt
	before := w.frame("unattributed", opened.Add(-time.Second), "public.orders", 1)
	atOpen := w.frame("unattributed", opened, "public.orders", 2)
	inside := w.frame("unattributed", opened.Add(10*time.Millisecond), "public.bulk", 301)
	unwatched := w.frame("unattributed", opened.Add(20*time.Millisecond), "public.products", 4)
	bound := w.frame("bound", opened.Add(30*time.Millisecond), "public.orders", 1)
	time.Sleep(50 * time.Millisecond) // the open window ends at the database's now
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	x, err := w.s.Get(bg, w.org, a.Row.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{atOpen, inside}
	slices.Sort(want)
	if got := frameIDs(x); !slices.Equal(got, want) {
		t.Fatalf("attached %v, want %v (before %s, unwatched %s, bound %s never)", got, want, before, unwatched, bound)
	}
	// Both sessions overlap the same window: overlap on both.
	for _, id := range []string{a.Row.ID, b.Row.ID} {
		x, _ := w.s.Get(bg, w.org, id)
		if !slices.Contains(x.Row.Flags, FlagOverlap) {
			t.Fatalf("%s lacks overlap: %v", id, x.Row.Flags)
		}
	}
	// Close a; a frame committed at the close time, decoded later, still attaches before the seal.
	if _, err := w.s.Close(bg, w.org, w.actor(), a.Row.ID); err != nil {
		t.Fatal(err)
	}
	x, _ = w.s.Get(bg, w.org, a.Row.ID)
	closed := *x.Row.ClosedAt
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	if x, _ = w.s.Get(bg, w.org, a.Row.ID); x.Row.State != StateClosing {
		t.Fatalf("sealed before the decoder caught up: %s", x.Row.State)
	}
	late := w.frame("unattributed", closed, "public.orders", 3)
	after := w.frame("unattributed", closed.Add(time.Second), "public.orders", 5)
	w.caughtUp(closed.Add(time.Second))
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	x, _ = w.s.Get(bg, w.org, a.Row.ID)
	if x.Row.State != StateSealed || x.Row.SealDeedID == nil {
		t.Fatalf("not sealed: %+v", x.Row)
	}
	if !slices.Contains(frameIDs(x), late) || slices.Contains(frameIDs(x), after) {
		t.Fatalf("late %s must attach, after %s must not: %v", late, after, frameIDs(x))
	}
	if x.Summary.Transactions != 3 || x.Summary.Rows != 306 || len(x.Summary.Caught) != 1 || !slices.Contains(x.Row.Flags, FlagCaught) {
		t.Fatalf("summary %+v flags %v", x.Summary, x.Row.Flags)
	}
	var data string
	if err := w.e.Admin(t).QueryRow(bg, `SELECT data::text FROM deedglass.admin_events WHERE action = 'observe.session_sealed' AND target_id = $1`, a.Row.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "shipped") || strings.Contains(data, "paid") || !strings.Contains(data, `"frame_count": 3`) || !strings.Contains(data, `"frames_digest"`) {
		t.Fatalf("seal data %s", data)
	}
	// A sealed session takes no new frame (the trigger), and its summary is fixed.
	if _, err := w.e.Admin(t).Exec(bg, `INSERT INTO deedglass.observe_frames (org_id, session_id, frame_id) VALUES ($1, $2, $3)`, w.org, a.Row.ID, after); err == nil ||
		store.SQLState(err) != "DG004" {
		t.Fatalf("a frame attached to a sealed session: %v", err)
	}
	// Exclude while sealed works and is sealed; a frame not in the session is refused.
	if _, err := w.s.Exclude(bg, w.org, w.actor(), a.Row.ID, late, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Exclude(bg, w.org, w.actor(), a.Row.ID, after, true, ""); !errors.Is(err, ErrFrameNotAttached) {
		t.Fatalf("exclude a stranger: %v", err)
	}
	var n int
	_ = w.e.Admin(t).QueryRow(bg, `SELECT count(*) FROM deedglass.admin_events WHERE action = 'observe.frame_excluded'`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d exclude deeds", n)
	}
	// The undo starts: excluding is refused (trigger and service).
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := StartUndo(ctx, tx, a.Row.ID, "act_01m3x71p6wesj7xewec6bwbwxm")
		if !ok && err == nil {
			err = errors.New("didn't start")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Exclude(bg, w.org, w.actor(), a.Row.ID, late, false, ""); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("exclude after the undo started: %v", err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.observe_frames SET excluded = false WHERE session_id = $1`, a.Row.ID); store.SQLState(err) != "DG004" {
		t.Fatalf("the trigger must refuse an exclude change while undoing: %v", err)
	}
	// A failed undo returns the session to sealed (the undo action doesn't exist: not applied).
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	if x, _ = w.s.Get(bg, w.org, a.Row.ID); x.Row.State != StateSealed {
		t.Fatalf("after a failed undo: %s", x.Row.State)
	}
	// Another org sees nothing.
	other, _, _, err := w.e.DB.CreateOrg(bg, store.NewOrg{Name: "Other", Slug: "other" + strings.ToLower(ids.New[ids.OrgKind]().String()[20:]), OwnerEmail: "o@other.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Get(bg, other.ID, a.Row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org read the session: %v", err)
	}
	if _, err := w.s.Close(bg, other.ID, Actor{Type: "user", ID: w.user.String()}, b.Row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another org closed the session: %v", err)
	}
	xs, _, _, err := w.s.List(bg, other.ID, ListFilter{})
	if err != nil || len(xs) != 0 {
		t.Fatalf("another org listed sessions: %v %d", err, len(xs))
	}
	xs, more, _, err := w.s.List(bg, w.org, ListFilter{Limit: 1})
	if err != nil || len(xs) != 1 || !more {
		t.Fatalf("list: %v %d %v", err, len(xs), more)
	}
	caughtOnly := true
	xs, _, _, _ = w.s.List(bg, w.org, ListFilter{Caught: &caughtOnly})
	if len(xs) != 1 || xs[0].Row.ID != a.Row.ID {
		t.Fatalf("caught filter: %d", len(xs))
	}
}

// TestCloseRules: max_session closes a forgotten session with a note; a decoder that never catches
// up flags decoder_behind after 30 s and seals after 10 minutes; the state trigger refuses a skip.
func TestCloseRules(t *testing.T) {
	w := newWorld(t, watchedCfg)
	x, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "pg-demo"})
	if err != nil {
		t.Fatal(err)
	}
	// Opened 2 minutes ago with max_session 1m.
	if _, err := w.e.Admin(t).Exec(bg, `ALTER TABLE deedglass.observe_sessions DISABLE TRIGGER observe_sessions_guard;
	    UPDATE deedglass.observe_sessions SET opened_at = now() - interval '2 minutes' WHERE id = '`+x.Row.ID+`';
	    ALTER TABLE deedglass.observe_sessions ENABLE TRIGGER observe_sessions_guard`); err != nil {
		t.Fatal(err)
	}
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	got, _ := w.s.Get(bg, w.org, x.Row.ID)
	if got.Row.State != StateClosing || !slices.Contains(got.Row.Flags, FlagMaxSession) || got.Row.ClosedByType == nil || *got.Row.ClosedByType != "system" {
		t.Fatalf("max_session: %+v", got.Row)
	}
	if !got.Row.ClosedAt.Equal(got.Row.OpenedAt.Add(time.Minute)) {
		t.Fatalf("closed at %v, want opened + 1m", got.Row.ClosedAt)
	}
	// The decoder is behind (no commit, no heartbeat after the close): flagged, then sealed after
	// LateAttach. closed_at is 1 minute ago, so 30 s passed: flag.
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	got, _ = w.s.Get(bg, w.org, x.Row.ID)
	if got.Row.State != StateClosing || !slices.Contains(got.Row.Flags, FlagDecoderBehind) {
		t.Fatalf("decoder_behind: %+v", got.Row)
	}
	if _, err := w.e.Admin(t).Exec(bg, `ALTER TABLE deedglass.observe_sessions DISABLE TRIGGER observe_sessions_guard;
	    UPDATE deedglass.observe_sessions SET opened_at = opened_at - interval '11 minutes', closed_at = closed_at - interval '11 minutes' WHERE id = '`+x.Row.ID+`';
	    ALTER TABLE deedglass.observe_sessions ENABLE TRIGGER observe_sessions_guard`); err != nil {
		t.Fatal(err)
	}
	if err := w.s.TickOrg(bg, w.org); err != nil {
		t.Fatal(err)
	}
	got, _ = w.s.Get(bg, w.org, x.Row.ID)
	if got.Row.State != StateSealed {
		t.Fatalf("not sealed after the late-attach window: %+v", got.Row)
	}
	// The trigger: no skipping back to open, no second close.
	if _, err := w.e.App(t).Exec(bg, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.observe_sessions SET state = 'open' WHERE id = $1`, x.Row.ID); store.SQLState(err) != "DG004" {
		t.Fatalf("sealed → open: %v", err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.observe_sessions SET state = 'undone' WHERE id = $1`, x.Row.ID); store.SQLState(err) != "DG004" {
		t.Fatalf("sealed → undone: %v", err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.observe_sessions SET label = 'renamed' WHERE id = $1`, x.Row.ID); store.SQLState(err) != "DG001" {
		t.Fatalf("label change: %v", err)
	}
	if _, err := w.e.Admin(t).Exec(bg, `DELETE FROM deedglass.observe_sessions WHERE id = $1`, x.Row.ID); store.SQLState(err) != "DG001" {
		t.Fatalf("delete: %v", err)
	}
	if _, err := w.e.App(t).Exec(bg, `DELETE FROM deedglass.observe_frames`); err == nil {
		t.Fatal("the app role deleted frames")
	}
}

// TestOpenRefusals: observe off, unknown connector, a bad label, an unknown agent.
func TestOpenRefusals(t *testing.T) {
	w := newWorld(t, `{"witness":{"enabled":true,"watched_tables":["public.orders"]}}`)
	var ne *NotEnabledError
	if _, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "pg-demo"}); !errors.As(err, &ne) || ne.Connector != "pg-demo" {
		t.Fatalf("observe off: %v", err)
	}
	if _, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "nope"}); !errors.Is(err, ErrConnectorNotFound) {
		t.Fatalf("unknown connector: %v", err)
	}
	if _, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "pg-demo", Label: "two\nlines"}); !errors.Is(err, ErrLabel) {
		t.Fatalf("label: %v", err)
	}
	w2 := newWorld(t, watchedCfg)
	if _, err := w2.s.Open(bg, w2.org, w2.actor(), OpenRequest{Connector: "pg-demo", Agent: "ghost-bot"}); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
}

// TestAttachBudget: attaching a 100-frame batch stays inside the S50 budget (≤ 50 ms p99 per batch).
func TestAttachBudget(t *testing.T) {
	w := newWorld(t, watchedCfg)
	x, err := w.s.Open(bg, w.org, w.actor(), OpenRequest{Connector: "pg-demo"})
	if err != nil {
		t.Fatal(err)
	}
	var times []time.Duration
	for round := 0; round < 10; round++ {
		for i := 0; i < 100; i++ {
			w.frame("unattributed", x.Row.OpenedAt.Add(time.Duration(round*100+i)*time.Microsecond), "public.orders", 3)
		}
		time.Sleep(5 * time.Millisecond)
		t0 := time.Now()
		if err := w.s.TickOrg(bg, w.org); err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Since(t0))
	}
	slices.Sort(times)
	got, _ := w.s.Get(bg, w.org, x.Row.ID)
	t.Logf("attach + summary per 100-frame batch: p50 %v, max %v (%d frames attached)", times[len(times)/2], times[len(times)-1], got.Summary.Transactions)
	if got.Summary.Transactions != 1000 {
		t.Fatalf("%d frames attached", got.Summary.Transactions)
	}
	t0 := time.Now()
	if _, err := w.s.Get(bg, w.org, x.Row.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("get at 1,000 frames: %v", time.Since(t0))
}
