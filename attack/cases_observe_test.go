//go:build attack

package attack

import (
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// Observe mode: O01–O02 (S50 §6). A watched session is a recording of what Postgres committed; the
// attacks try to reach another org's recording, or to let an agent hide its own: open, close, edit
// or undo it with the agent's key, or leave out a transaction without a trace.

// observeOn turns observe mode on for Acme's postgres connector and points the decoder at the clone.
func observeOn(h *harness.H) {
	id := h.Acme.Conns["postgres"]
	cfg, _ := h.Acme.Admin.Do("GET", "/v1/connectors/"+id, nil).Map()["config"].(map[string]any)
	w, _ := cfg["witness"].(map[string]any)
	if w == nil {
		panic("the harness's postgres connector has no witness config")
	}
	w["observe"] = map[string]any{"enabled": true}
	if r := h.Acme.Admin.Do("PATCH", "/v1/connectors/"+id, map[string]any{"config": cfg}); r.Status != 200 {
		panic(fmt.Sprintf("turning observe mode on: HTTP %d %s", r.Status, r.Body))
	}
	h.L2Ready()
}

func openSession(h *harness.H) string {
	r := h.Acme.Admin.Do("POST", "/v1/observe/sessions", map[string]any{"connector": "postgres", "label": "attack"})
	id := harness.Str(r.Map(), "id")
	if id == "" {
		panic(fmt.Sprintf("open: HTTP %d %s", r.Status, r.Body))
	}
	return id
}

// sealedSession opens a session, writes two orders on the clone (two transactions), closes it and
// waits for the seal. It returns the session and its newest transaction.
func sealedSession(h *harness.H) (string, string) {
	id := openSession(h)
	mustExec(h, `UPDATE public.orders SET status = 'cancelled' WHERE id = 41`)
	mustExec(h, `UPDATE public.orders SET status = 'cancelled' WHERE id = 40`)
	time.Sleep(300 * time.Millisecond)
	h.Acme.Admin.Do("POST", "/v1/observe/sessions/"+id+"/close", map[string]any{})
	// A commit after the close tells the decoder's reader it has passed the close time.
	time.Sleep(100 * time.Millisecond)
	mustExec(h, `UPDATE public.orders SET status = status WHERE id = 39`)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		s := h.Acme.Admin.Do("GET", "/v1/observe/sessions/"+id, nil).Map()
		if harness.Str(s, "state") == "sealed" {
			tx, _ := harness.Get(s, "transactions", "data").([]any)
			if len(tx) > 1 {
				return id, harness.Str(tx[0], "id")
			}
			panic(fmt.Sprintf("the sealed session holds %d transactions, want 2: %v", len(tx), s))
		}
		time.Sleep(500 * time.Millisecond)
	}
	panic("the session never sealed")
}

func init() {
	register(
		harness.Case{ID: "O01", Run: o01},
		harness.Case{ID: "O02", Run: o02},
	)
}

// O01: Globex's admin key aims every session route at Acme's open session. Each answers exactly
// as for an unknown id, and the session is unchanged.
func o01(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	observeOn(h)
	id := openSession(h)
	unknown := "obs_01m3zzzzzzzzzzzzzzzzzzzzzz"
	dtx := "dtx_01m3zzzzzzzzzzzzzzzzzzzzzz"
	type call struct {
		method, path string
		body         any
	}
	calls := func(s string) []call {
		return []call{
			{"POST", "/v1/observe/sessions/" + s + "/close", map[string]any{}},
			{"POST", "/v1/observe/sessions/" + s + "/frames/" + dtx + "/exclude", map[string]any{"excluded": true}},
			{"GET", "/v1/observe/sessions/" + s + "/undo_preview", nil},
			{"POST", "/v1/observe/sessions/" + s + "/undo", map[string]any{"plan_digest": "sha256:" + strings.Repeat("0", 64), "reason": "O01"}},
			{"GET", "/v1/observe/sessions/" + s, nil},
		}
	}
	ok := true
	theirs, ctl := calls(id), calls(unknown)
	for i := range theirs {
		r := h.Globex.Admin.Do(theirs[i].method, theirs[i].path, theirs[i].body)
		c := h.Globex.Admin.Do(ctl[i].method, ctl[i].path, ctl[i].body)
		obs.Evidence[theirs[i].method+" "+strings.Replace(theirs[i].path, id, "{acme}", 1)] = refused(r)
		if !is(r, 404, "not_found") || r.Status != c.Status || r.ErrorCode() != c.ErrorCode() {
			ok = false
		}
	}
	s := h.Acme.Admin.Do("GET", "/v1/observe/sessions/"+id, nil).Map()
	state := harness.Str(s, "state")
	obs.Evidence["acme_state"] = state
	if ok && state == "open" {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (not_found for close, exclude, preview and undo, exactly as an unknown id; the session stays open with nothing excluded)"
		obs.Line = "not_found · same as an unknown id · the session stays open"
	} else {
		obs.Outcome, obs.Detail = "REJECTED?", fmt.Sprintf("answers %v, session %s", obs.Evidence, state)
	}
	h.Acme.Admin.Do("POST", "/v1/observe/sessions/"+id+"/close", map[string]any{})
	return obs
}

// O02: the agent's own key tries to open, close, exclude and undo; then a person leaves the
// transaction out, which is sealed and named wherever the undo is shown.
func o02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: lForbidden, TargetMayChange: true}
	observeOn(h)
	id, dtx := sealedSession(h)
	agent := h.Acme.Agents["attacker"].API
	tries := []harness.Response{
		agent.Do("POST", "/v1/observe/sessions", map[string]any{"connector": "postgres"}),
		agent.Do("POST", "/v1/observe/sessions/"+id+"/close", map[string]any{}),
		agent.Do("POST", "/v1/observe/sessions/"+id+"/frames/"+dtx+"/exclude", map[string]any{"excluded": true}),
		agent.Do("POST", "/v1/observe/sessions/"+id+"/undo", map[string]any{"plan_digest": "sha256:" + strings.Repeat("0", 64), "reason": "O02"}),
		agent.Do("GET", "/v1/observe/sessions/"+id+"/undo_preview", nil),
	}
	denied := true
	for i, r := range tries {
		obs.Evidence[fmt.Sprintf("agent_%d", i)] = refused(r)
		denied = denied && is(r, 403, "insufficient_scope")
	}
	ex := h.Acme.Admin.Do("POST", "/v1/observe/sessions/"+id+"/frames/"+dtx+"/exclude", map[string]any{"excluded": true})
	sealed := h.Env.CPQuery(`SELECT count(*)::text FROM deedglass.admin_events WHERE action = 'observe.frame_excluded' AND target_id = $1 AND deed_id IS NOT NULL`, id)[0][0]
	pv := h.Acme.Admin.Do("GET", "/v1/observe/sessions/"+id+"/undo_preview", nil)
	pm := pv.Map()
	exl, _ := harness.Get(pm, "excluded").([]any)
	listed := len(exl) == 1 && exl[0] == dtx && strings.Contains(harness.Str(pm, "sql"), "Left out by a person (not undone): "+dtx)
	obs.Evidence["exclude"], obs.Evidence["sealed_excludes"], obs.Evidence["preview_lists_it"] = ex.Status, sealed, listed
	obs.Evidence["preview_excluded"], obs.Evidence["preview_sql"], obs.Evidence["preview_refusals"] = harness.Get(pm, "excluded"), harness.Get(pm, "sql") != nil, harness.Get(pm, "refusals")
	if denied && ex.Status == 200 && sealed == "1" && listed {
		obs.Outcome = "DENIED"
		obs.Detail = "DENIED (403 insufficient_scope for every agent-key call; the person's exclude is sealed as observe.frame_excluded and the undo preview and its SQL list the transaction left out)"
		obs.Line = "insufficient_scope ×5 · the exclude is sealed and listed"
	} else {
		obs.Outcome, obs.Detail = "DENIED?", fmt.Sprintf("%v", obs.Evidence)
	}
	return obs
}
