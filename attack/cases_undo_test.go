//go:build attack

package attack

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// Undo: U01–U05 (S23 §6). An undo writes to the customer's database on a person's request, so
// every attack tries to make it write something Postgres didn't record, or to reach it without a
// person: rows that change under it, values smuggled into the request, a forged marker naming the
// action, an agent calling the internal tool, another org's action.
//
// Each case ships order 42 as witness-bot (L2 · VERIFIED on the clone), then works on that action.
// The attack-baseline rule undo-needs-approval asks for a countersign, which Acme's admin ana gives
// from her session.

// undoTarget ships order 42 and returns the sealed, L2-witnessed action.
func undoTarget(h *harness.H) string {
	mustExec(h, `UPDATE public.orders SET status = 'paid' WHERE id = 42`)
	h.L2Ready()
	id, w := l2Act(h, shipFortyTwo)
	if harness.Str(w, "verdict") != "VERIFIED" || harness.Str(w, "level") != "L2" {
		panic(fmt.Sprintf("the undo target isn't L2 VERIFIED: %v", w))
	}
	if pv := undoPreview(h.Acme, id).Map(); harness.Get(pv, "available") != true {
		var failed []string
		cs, _ := harness.Get(pv, "checks").([]any)
		for _, c := range cs {
			if m, _ := c.(map[string]any); harness.Str(m, "status") == "fail" {
				failed = append(failed, harness.Str(m, "id")+": "+harness.Str(m, "detail"))
			}
		}
		panic(fmt.Sprintf("the undo of the target isn't available: %v", failed))
	}
	return id
}

func undoPreview(o *harness.Org, act string) harness.Response {
	return o.Admin.Do("POST", "/v1/actions/"+act+"/undo/preview", map[string]any{})
}

func undoRun(o *harness.Org, act string, body map[string]any) harness.Response {
	return o.Admin.Do("POST", "/v1/actions/"+act+"/undo", body)
}

// countersign approves apr as ana (an admin who isn't the requester).
func countersign(h *harness.H, apr string) harness.Response {
	_, ana, _ := approvers(h)
	dg := harness.Str(approval(h, apr), "context_digest")
	return ana.Session.Do("POST", "/v1/approvals/"+apr+"/approve", map[string]any{"context_digest": dg})
}

// statusOf is an order's status on the clone.
func statusOf(h *harness.H, id int) string {
	c := h.Target.Conn()
	defer c.Close(context.Background())
	var s string
	_ = c.QueryRow(context.Background(), `SELECT status FROM public.orders WHERE id = $1`, id).Scan(&s)
	return s
}

func init() {
	register(
		harness.Case{ID: "U01", Run: u01},
		harness.Case{ID: "U02", Run: u02},
		harness.Case{ID: "U03", Run: u03},
		harness.Case{ID: "U04", Run: u04},
		harness.Case{ID: "U05", Run: u05},
	)
}

// U01: a writer changes order 42 after the undo was planned and before it runs. The undo locks
// the row, sees it changed, and Postgres rolls the whole undo back: failed, undo_conflict, no row
// changed, no ledger row.
func u01(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	act := undoTarget(h)
	obs.ActionIDs = append(obs.ActionIDs, act)
	pv := undoPreview(h.Acme, act).Map()
	digest := harness.Str(pv, "plan_digest")
	r := undoRun(h.Acme, act, map[string]any{"plan_digest": digest, "reason": "U01: put order 42 back.", "wait": 0})
	undo, apr := harness.Str(r.Map(), "id"), harness.Str(r.Map(), "approval", "id")
	if undo == "" || apr == "" {
		panic(fmt.Sprintf("the undo didn't pause for approval: HTTP %d %s", r.Status, r.Body))
	}
	obs.ActionIDs = append(obs.ActionIDs, undo)
	mustExec(h, `UPDATE public.orders SET status = 'cancelled' WHERE id = 42`)
	ledger, fp := h.Target.LedgerRows(), h.Target.Fingerprint()
	c := countersign(h, apr)
	a := h.WaitSealed(h.Acme, undo)
	obs.Evidence["countersign"] = c.Status
	obs.Evidence["undo"] = map[string]any{"status": harness.Str(a, "status"), "error_code": harness.Str(a, "execution", "error_code"),
		"error_message": harness.Str(a, "execution", "error_message")}
	after42, ledgerAfter, fpAfter := statusOf(h, 42), h.Target.LedgerRows(), h.Target.Fingerprint()
	obs.Evidence["order_42"], obs.Evidence["ledger_rows"] = after42, []int{ledger, ledgerAfter}
	if c.Status == 200 && harness.Str(a, "status") == "failed" && harness.Str(a, "execution", "error_code") == "undo_conflict" &&
		after42 == "cancelled" && ledgerAfter == ledger && fpAfter == fp {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (undo_conflict; the undo is failed and Postgres changed no row)"
		obs.Line = "undo_conflict · order 42 stays cancelled · no ledger row"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("undo %s/%s, order 42 %s, ledger rows %d → %d, rows changed %v", harness.Str(a, "status"),
			harness.Str(a, "execution", "error_code"), after42, ledger, ledgerAfter, fpAfter != fp)
	}
	return obs
}

// U02: the undo request carries values (a field beyond its four) or a digest Deedglass didn't plan.
// The plan comes only from Postgres's row images, so both are refused and no row changes.
func u02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	act := undoTarget(h)
	obs.ActionIDs = append(obs.ActionIDs, act)
	digest := harness.Str(undoPreview(h.Acme, act).Map(), "plan_digest")
	fp := h.Target.Fingerprint()
	smuggled := undoRun(h.Acme, act, map[string]any{"plan_digest": digest, "reason": "U02", "set": map[string]any{"status": "refunded"}})
	tampered := digest[:len(digest)-1] + map[bool]string{true: "1", false: "0"}[strings.HasSuffix(digest, "0")]
	stale := undoRun(h.Acme, act, map[string]any{"plan_digest": tampered, "reason": "U02"})
	asAgent := witnessBot(h).API.Do("POST", "/v1/actions/"+act+"/undo", map[string]any{"plan_digest": digest, "reason": "U02"})
	obs.Evidence["smuggled"], obs.Evidence["tampered"], obs.Evidence["agent_key"] = refused(smuggled), refused(stale), refused(asAgent)
	unchanged := h.Target.Fingerprint() == fp
	live := harness.Str(h.Action(h.Acme, act), "undo", "pending") + harness.Str(h.Action(h.Acme, act), "undo", "undone_by")
	obs.Evidence["rows_unchanged"], obs.Evidence["undo_started"] = unchanged, live != ""
	if is(smuggled, 400, "validation_failed") && is(stale, 409, "undo_plan_stale") && is(asAgent, 403, "insufficient_scope") && unchanged && live == "" {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (validation_failed for any field beyond plan_digest, reason, allow_triggers and wait; undo_plan_stale for a digest Deedglass didn't plan; no row changes)"
		obs.Line = "validation_failed · undo_plan_stale · insufficient_scope · no row changed"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("smuggled %v, tampered %v, agent %v, rows unchanged %v, undo started %v", refused(smuggled), refused(stale),
			refused(asAgent), unchanged, live != "")
	}
	return obs
}

// U03: another session emits a transactional deedglass marker naming the action, in a transaction
// that cancels order 7. The decoder records it as forged (no ledger row), so it never becomes the
// action's evidence: the undo plans only order 42, and order 7 stays cancelled.
func u03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	act := undoTarget(h)
	obs.ActionIDs = append(obs.ActionIDs, act)
	wait, stop := h.Env.ListenAlerts("forged_marker")
	defer stop()
	mustExec(h, `BEGIN; SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', '`+act+`'); UPDATE public.orders SET status = 'cancelled' WHERE id = 7; COMMIT;`)
	alerted := wait(10 * time.Second)
	pv := undoPreview(h.Acme, act).Map()
	steps, _ := harness.Get(pv, "steps", "data").([]any)
	var keys []string
	for _, s := range steps {
		keys = append(keys, harness.Str(s.(map[string]any), "key", "id"))
	}
	obs.Evidence["alert"], obs.Evidence["preview"] = alerted, map[string]any{"available": harness.Get(pv, "available"), "rows": harness.Get(pv, "rows"), "keys": keys}
	r := undoRun(h.Acme, act, map[string]any{"plan_digest": harness.Str(pv, "plan_digest"), "reason": "U03: put order 42 back.", "wait": 0})
	undo, apr := harness.Str(r.Map(), "id"), harness.Str(r.Map(), "approval", "id")
	if undo == "" || apr == "" {
		panic(fmt.Sprintf("the undo didn't pause for approval: HTTP %d %s", r.Status, r.Body))
	}
	obs.ActionIDs = append(obs.ActionIDs, undo)
	countersign(h, apr)
	w := h.WitnessOf(h.Acme, undo)
	o42, o7 := statusOf(h, 42), statusOf(h, 7)
	obs.Evidence["undo"] = witnessEvidence(undo, w)
	obs.Evidence["order_42"], obs.Evidence["order_7"] = o42, o7
	if alerted && harness.Get(pv, "available") == true && len(keys) == 1 && keys[0] == "42" && harness.Str(w, "verdict") == "VERIFIED" &&
		o42 == "paid" && o7 == "cancelled" {
		obs.Outcome = "REJECTED"
		obs.Detail = "REJECTED (the forged transaction never becomes evidence: the plan holds only the action's own bound transaction, and the forged rows stay as they are)"
		obs.Line = "forged_marker alert · plan holds order 42 only · undo L2 VERIFIED · order 7 untouched"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("alert %v, preview keys %v, undo %s, order 42 %s, order 7 %s", alerted, keys, harness.Str(w, "verdict"), o42, o7)
	}
	return obs
}

// U04: an agent names the internal undo tool through POST /v1/actions. It is recorded and sealed
// as tool_unknown, and nothing reaches Postgres.
func u04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	act := undoTarget(h)
	obs.ActionIDs = append(obs.ActionIDs, act)
	digest := harness.Str(undoPreview(h.Acme, act).Map(), "plan_digest")
	ledger, fp := h.Target.LedgerRows(), h.Target.Fingerprint()
	r, id := h.Act(witnessBot(h), "postgres.undo", map[string]any{"undoes": act, "plan_digest": digest, "allow_triggers": true, "reason": "U04"})
	obs.Evidence["call"] = refused(r)
	if id != "" {
		obs.ActionIDs = append(obs.ActionIDs, id)
	}
	a := h.WaitSealed(h.Acme, id)
	rule := harness.Str(a, "decision", "rule_id")
	unchanged := h.Target.LedgerRows() == ledger && h.Target.Fingerprint() == fp
	obs.Evidence["sealed"], obs.Evidence["rule"], obs.Evidence["rows_unchanged"] = harness.Str(a, "state"), rule, unchanged
	if id != "" && r.ErrorCode() == "tool_unknown" && harness.Str(a, "state") == "sealed" && harness.Str(a, "status") == "denied" && unchanged {
		obs.Outcome, obs.Detail = "DENIED", "DENIED (tool_unknown, recorded and sealed; nothing reaches Postgres)"
		obs.Line = "tool_unknown · sealed · no ledger row"
	} else {
		obs.Outcome = "DENIED?"
		obs.Detail = fmt.Sprintf("HTTP %d %s, state %s, status %s, rows unchanged %v", r.Status, r.ErrorCode(), harness.Str(a, "state"),
			harness.Str(a, "status"), unchanged)
	}
	return obs
}

// U05: Globex's admin key previews and runs an undo of Acme's action. Both answer exactly as for an
// id that doesn't exist, and nothing is planned.
func u05(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	act := undoTarget(h)
	obs.ActionIDs = append(obs.ActionIDs, act)
	digest := harness.Str(undoPreview(h.Acme, act).Map(), "plan_digest")
	unknown := "act_01m3zzzzzzzzzzzzzzzzzzzzzz"
	pv := undoPreview(h.Globex, act)
	pvUnknown := undoPreview(h.Globex, unknown)
	run := undoRun(h.Globex, act, map[string]any{"plan_digest": digest, "reason": "U05"})
	runUnknown := undoRun(h.Globex, unknown, map[string]any{"plan_digest": digest, "reason": "U05"})
	obs.Evidence["preview"], obs.Evidence["run"] = refused(pv), refused(run)
	live := harness.Str(h.Action(h.Acme, act), "undo", "pending") + harness.Str(h.Action(h.Acme, act), "undo", "undone_by")
	same := pv.Status == pvUnknown.Status && pv.ErrorCode() == pvUnknown.ErrorCode() && run.Status == runUnknown.Status &&
		run.ErrorCode() == runUnknown.ErrorCode()
	if is(pv, 404, "not_found") && is(run, 404, "not_found") && same && live == "" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (not_found for preview and run, exactly as an unknown id; nothing is planned)"
		obs.Line = "not_found · same as an unknown id"
	} else {
		obs.Outcome = "REJECTED?"
		obs.Detail = fmt.Sprintf("preview %v, run %v, same as unknown %v, undo started %v", refused(pv), refused(run), same, live != "")
	}
	return obs
}
