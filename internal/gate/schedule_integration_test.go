//go:build integration

package gate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store"
)

// S40 §6: scheduled execution after approval.

type vclock struct{ ns atomic.Int64 }

func (c *vclock) now() time.Time      { return time.Unix(0, c.ns.Load()).UTC() }
func (c *vclock) set(t time.Time)     { c.ns.Store(t.UnixNano()) }
func (c *vclock) add(d time.Duration) { c.ns.Add(int64(d)) }

func newClockWorld(t *testing.T, policy string) (*world, *vclock) {
	c := &vclock{}
	c.set(time.Now().UTC().Truncate(time.Second))
	return newWorld(t, worldOpts{clock: c.now, policy: policy}), c
}

func scheduled(tool, args string, nb, na time.Time) ActionRequest {
	r := req(tool, args)
	r.Schedule = &Schedule{NotBefore: nb, NotAfter: na}
	return r
}

func (w *world) approve(id string, at time.Time) {
	w.t.Helper()
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		var apr string
		if err := tx.QueryRow(ctx, `SELECT approval_id FROM deedglass.actions WHERE id = $1`, id).Scan(&apr); err != nil {
			return err
		}
		if err := store.DecideApproval(ctx, tx, w.org, apr, "approved", &by, nil, at); err != nil {
			return err
		}
		return store.EnqueueJob(ctx, tx, w.org, mustAction(id), JobApprovalResume, at, 8)
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) reasons(id string) []string {
	w.t.Helper()
	var out []string
	err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT COALESCE(data->>'reason', '') FROM deedglass.action_events WHERE action_id = $1 ORDER BY seq`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

const writeArgs = `{"rows":1,"table":"approvals"}`

func TestScheduleRunsInsideWindow(t *testing.T) {
	w, c := newClockWorld(t, "")
	nb, na := c.now().Add(2*time.Hour), c.now().Add(4*time.Hour)
	a := w.must(0, scheduled("fake.write", writeArgs, nb, na))
	if a.Status != "pending_approval" || a.Schedule == nil || !time.Time(a.Schedule.NotBefore).Equal(nb) {
		t.Fatalf("%s", mustJSON(a))
	}
	w.approve(a.ID, c.now())
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "pending_approval" || w.fake.Calls() != 0 || r.NextAction == nil || r.NextAction.Type != "scheduled" {
		t.Fatalf("ran before not_before: %s", mustJSON(r))
	}
	c.set(nb.Add(time.Minute))
	r, err = w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sealed" || r.Status != "executed" || w.fake.CallsFor(a.ID) != 1 {
		t.Fatalf("%s", mustJSON(r))
	}
}

func TestScheduleMissed(t *testing.T) {
	w, c := newClockWorld(t, "")
	nb, na := c.now().Add(time.Hour), c.now().Add(2*time.Hour)
	a := w.must(0, scheduled("fake.write", writeArgs, nb, na))
	c.set(na.Add(time.Minute))
	w.approve(a.ID, c.now())
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sealed" || r.Status != "cancelled" || w.fake.Calls() != 0 || r.Approval == nil || r.Approval.Status != "approved" {
		t.Fatalf("%s", mustJSON(r))
	}
	if rs := w.reasons(a.ID); !strings.Contains(strings.Join(rs, ","), ReasonScheduleMissed) {
		t.Fatalf("reasons %v", rs)
	}
	if err := path(w.events(a.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleAllowRules(t *testing.T) {
	w, c := newClockWorld(t, "")
	// allow with a window 2 h ahead: denied and sealed.
	a, err := w.submit(0, scheduled("fake.echo", `{}`, c.now().Add(2*time.Hour), c.now().Add(3*time.Hour)))
	he := refusal(t, a, err, httpx.CodeScheduleNeedsApproval, 403)
	if he.Message != MsgScheduleNeedsTap || w.fake.Calls() != 0 {
		t.Fatalf("%q", he.Message)
	}
	// allow with not_before within 60 s: runs now.
	b := w.must(0, scheduled("fake.echo", `{}`, c.now().Add(30*time.Second), c.now().Add(10*time.Minute)))
	if b.Status != "executed" || w.fake.Calls() != 1 || b.Schedule == nil {
		t.Fatalf("%s", mustJSON(b))
	}
	// proxy with a schedule: 400, nothing recorded.
	p := scheduled("scratch.run", `{}`, c.now(), c.now().Add(10*time.Minute))
	p.Mode, p.ConnectorType = ModeProxy, "sdk"
	if _, err := w.submit(0, p); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeValidationFailed || httpx.AsError(err).Param != "schedule" {
		t.Fatalf("proxy: %v", err)
	}
	// Shapes.
	for name, s := range map[string][2]time.Time{
		"span under 5 min": {c.now(), c.now().Add(4 * time.Minute)},
		"over 72 h":        {c.now(), c.now().Add(73 * time.Hour)},
		"past":             {c.now().Add(-2 * time.Hour), c.now().Add(-time.Hour)},
	} {
		if _, err := w.submit(0, scheduled("fake.write", writeArgs, s[0], s[1])); httpx.AsError(err) == nil || httpx.AsError(err).Param != "schedule" {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestCancelApprovedScheduled(t *testing.T) {
	w, c := newClockWorld(t, "")
	a := w.must(0, scheduled("fake.write", writeArgs, c.now().Add(time.Hour), c.now().Add(2*time.Hour)))
	w.approve(a.ID, c.now())
	r, err := w.p.Cancel(bg, w.pr(0), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "cancelled" || r.State != "sealed" || r.Approval.Status != "approved" {
		t.Fatalf("%s", mustJSON(r))
	}
	var jobs int
	w.adminRow(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1`, []any{a.ID}, &jobs)
	if jobs != 0 {
		t.Fatalf("%d jobs left", jobs)
	}
	c.add(90 * time.Minute)
	if r, _ := w.p.Resume(bg, w.org, mustAction(a.ID)); r.Status != "cancelled" || w.fake.Calls() != 0 {
		t.Fatalf("ran after cancel: %s", mustJSON(r))
	}
	// An unscheduled approved action keeps S22's refusal.
	b := w.must(0, req("fake.write", writeArgs))
	w.approve(b.ID, c.now())
	if _, err := w.p.Cancel(bg, w.pr(0), b.ID); httpx.AsError(err) == nil || httpx.AsError(err).Message != MsgCancelApproved {
		t.Fatalf("unscheduled: %v", err)
	}
	// A claimed job: 409 action_executing.
	d := w.must(0, scheduled("fake.write", writeArgs, c.now().Add(time.Hour), c.now().Add(2*time.Hour)))
	w.approve(d.ID, c.now())
	w.admin(`UPDATE deedglass.witness_jobs SET locked_by = 'test', locked_until = now() + interval '1 minute' WHERE action_id = $1`, d.ID)
	if _, err := w.p.Cancel(bg, w.pr(0), d.ID); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeActionExecuting {
		t.Fatalf("claimed: %v", err)
	}
}

func TestCancelRacesWorker(t *testing.T) {
	w, c := newClockWorld(t, "")
	runs := 200
	if testing.Short() {
		runs = 20
	}
	var cancelled, executed int
	for i := range runs {
		a := w.must(i%3, scheduled("fake.write", writeArgs, c.now().Add(time.Minute), c.now().Add(time.Hour)))
		w.approve(a.ID, c.now())
		c.add(2 * time.Minute) // inside the window
		// Stagger which side starts first (0 to 3 ms) so both orders and true overlaps occur.
		d := time.Duration(i%4) * time.Millisecond
		var wg sync.WaitGroup
		wg.Go(func() {
			if i%2 == 1 {
				time.Sleep(d)
			}
			_, _ = w.p.Cancel(bg, w.pr(i%3), a.ID)
		})
		wg.Go(func() {
			if i%2 == 0 {
				time.Sleep(d)
			}
			// The worker: claim the job (a lease), then resume, as handleOther does.
			var claimed bool
			err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `UPDATE deedglass.witness_jobs SET locked_by = 'w', locked_until = now() + interval '30 seconds'
				    WHERE action_id = $1 AND kind = 'approval_resume' AND locked_until IS NULL`, a.ID)
				claimed = err == nil && tag.RowsAffected() == 1
				return err
			})
			if err == nil && claimed {
				_, _ = w.p.Resume(bg, w.org, mustAction(a.ID))
			}
		})
		wg.Wait()
		r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
		if err != nil {
			t.Fatal(err)
		}
		calls := w.fake.CallsFor(a.ID)
		switch {
		case r.Status == "cancelled" && calls == 0:
			cancelled++
		case r.Status == "executed" && calls == 1:
			executed++
		default:
			t.Fatalf("run %d: status %s with %d calls", i, r.Status, calls)
		}
		c.add(-2 * time.Minute)
	}
	t.Logf("%d runs: %d cancelled, %d executed, 0 both", runs, cancelled, executed)
}

func TestStopAllBeforeWindow(t *testing.T) {
	w, c := newClockWorld(t, "")
	a := w.must(0, scheduled("fake.write", writeArgs, c.now().Add(time.Hour), c.now().Add(2*time.Hour)))
	w.approve(a.ID, c.now())
	w.admin(`UPDATE deedglass.orgs SET stopped_at = now() WHERE id = $1`, w.org)
	c.add(61 * time.Minute)
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "denied" || w.fake.Calls() != 0 {
		t.Fatalf("%s", mustJSON(r))
	}
}

func TestScheduleNeverChanges(t *testing.T) {
	w, c := newClockWorld(t, "")
	a := w.must(0, scheduled("fake.write", writeArgs, c.now().Add(time.Hour), c.now().Add(2*time.Hour)))
	_, err := w.e.Admin(t).Exec(bg, `UPDATE deedglass.actions SET schedule = '{"not_before":"2030-01-01T00:00:00Z","not_after":"2030-01-01T01:00:00Z"}' WHERE id = $1`, a.ID)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "DG004" {
		t.Fatalf("superuser update: %v", err)
	}
	err = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET schedule = NULL WHERE id = $1`, a.ID)
		return err
	})
	if !errors.As(err, &pe) || pe.Code != "42501" {
		t.Fatalf("app role update: %v", err)
	}
}
