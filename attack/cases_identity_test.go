//go:build attack

package attack

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// Identity, intents and budgets: J01–J04 (S18 §3.4). A stolen or misused agent key, a replayed
// signed intent, and a burst past the agent's budget.

func echo(msg string) map[string]any { return map[string]any{"message": msg} }

// refusedWithin calls fake.echo as a until the answer carries code, for up to 2 s.
func refusedWithin(h *harness.H, a *harness.Agent, code string) (harness.Response, time.Duration, string) {
	start := time.Now()
	for {
		r, id := h.Act(a, "fake.echo", echo("after"))
		if r.ErrorCode() == code || time.Since(start) > 2*time.Second {
			return r, time.Since(start), id
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func rejected(h *harness.H, a *harness.Agent, code string, revoke func()) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"orders", "other", "secret-echo", "mcp"}}
	r, id := h.Act(a, "fake.echo", echo("before"))
	before := h.WaitSealed(h.Acme, id)
	obs.ActionIDs = append(obs.ActionIDs, id)
	obs.Evidence["before"] = map[string]any{"status": r.Status, "execution": harness.Str(before, "execution", "status")}
	if harness.Str(before, "execution", "status") != "succeeded" {
		h.Problem("the key didn't work before the change: HTTP %d %s", r.Status, r.Body)
	}
	revoke()
	r, took, refusedID := refusedWithin(h, a, code)
	obs.Evidence["after"] = map[string]any{"status": r.Status, "error.code": r.ErrorCode(), "took_ms": took.Milliseconds(),
		"message": harness.Str(r.Map(), "error", "message"), "action": refusedID}
	if refusedID != "" {
		// A refusal may be recorded and sealed; it must never have run.
		obs.ActionIDs = append(obs.ActionIDs, refusedID)
		if a := h.WaitSealed(h.Acme, refusedID); harness.Get(a, "execution", "started_at") != nil {
			h.Problem("the refused call started executing")
		}
	}
	if (r.Status == 401 || r.Status == 403) && r.ErrorCode() == code && took <= 2*time.Second {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED ("+code+")"
		obs.Line = fmt.Sprintf("%s · refused in %d ms", code, took.Milliseconds())
	} else {
		obs.Outcome, obs.Detail = fmt.Sprintf("HTTP %d", r.Status), "got "+r.ErrorCode()
	}
	return obs
}

func init() {
	register(
		harness.Case{ID: "J01", Run: func(h *harness.H) harness.Observed {
			a := h.Acme.Agents["suspend-bot"]
			return rejected(h, a, "agent_suspended", func() {
				r := h.Acme.Admin.Do("POST", "/v1/agents/"+a.ID+"/suspend", map[string]string{"reason": "Attack suite J01."})
				if r.Status != 200 {
					h.Problem("suspend: HTTP %d %s", r.Status, r.Body)
				}
			})
		}},
		harness.Case{ID: "J02", Run: func(h *harness.H) harness.Observed {
			a := h.Acme.Agents["revoke-bot"]
			return rejected(h, a, "api_key_revoked", func() {
				r := h.Acme.Admin.Do("POST", "/v1/keys/"+a.KeyID+"/revoke", map[string]string{"reason": "Attack suite J02."})
				if r.Status != 200 {
					h.Problem("revoke: HTTP %d %s", r.Status, r.Body)
				}
			})
		}},
		harness.Case{ID: "J03", Run: j03},
		harness.Case{ID: "J04", Run: j04},
	)
}

// J03: intent_mode required; one signed intent used twice in a row, then a fresh one sent 16
// times at once. Exactly one use of each executes.
func j03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"orders", "other", "secret-echo", "mcp"}}
	a := h.Acme.Agents["intent-bot"]
	pem, pub, _, err := deedglass.GenerateIntentKey()
	if err != nil {
		panic(err)
	}
	if r := h.Acme.Admin.Do("POST", "/v1/agents/"+a.ID+"/intent-keys", map[string]string{"public_key": pub}); r.Status != 201 {
		panic(fmt.Sprintf("register the intent key: HTTP %d %s", r.Status, r.Body))
	}
	if r := h.Acme.Admin.Do("PATCH", "/v1/agents/"+a.ID, map[string]string{"intent_mode": "required"}); r.Status != 200 {
		panic(fmt.Sprintf("intent_mode required: HTTP %d %s", r.Status, r.Body))
	}
	signer, err := deedglass.LoadIntentSigner(pem, h.Acme.ID, a.ID)
	if err != nil {
		panic(err)
	}
	args := echo("signed")
	raw, _ := json.Marshal(args)
	sign := func() string {
		hdr, _, err := signer.Sign("fake.echo", raw, deedglass.SignOptions{TTL: time.Minute})
		if err != nil {
			panic(err)
		}
		return hdr
	}
	// Without an intent the call is refused (the mode is really required).
	r0, id0 := h.Act(a, "fake.echo", args)
	obs.Evidence["unsigned"] = map[string]any{"status": r0.Status, "error.code": r0.ErrorCode()}
	if id0 != "" {
		obs.ActionIDs = append(obs.ActionIDs, id0)
	}
	// Sequential replay.
	intent := sign()
	r1, id1 := h.Act(a, "fake.echo", args, "Deedglass-Intent", intent)
	first := h.WaitSealed(h.Acme, id1)
	obs.ActionIDs = append(obs.ActionIDs, id1)
	r2, id2 := h.Act(a, "fake.echo", args, "Deedglass-Intent", intent)
	if id2 != "" {
		obs.ActionIDs = append(obs.ActionIDs, id2)
	}
	obs.Evidence["sequential"] = map[string]any{"first": map[string]any{"status": r1.Status, "execution": harness.Str(first, "execution", "status")},
		"replay": map[string]any{"status": r2.Status, "error.code": r2.ErrorCode()}}
	seqOK := harness.Str(first, "execution", "status") == "succeeded" && r2.Status == 403 && r2.ErrorCode() == "intent_replay"
	// 16 at once with one fresh intent.
	intent = sign()
	var wg sync.WaitGroup
	gate := make(chan struct{})
	type out struct {
		status int
		code   string
		id     string
	}
	outs := make([]out, 16)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			r, id := h.Act(a, "fake.echo", args, "Deedglass-Intent", intent)
			outs[i] = out{r.Status, r.ErrorCode(), id}
		}(i)
	}
	close(gate)
	wg.Wait()
	executed, replays := 0, 0
	for _, o := range outs {
		if o.id != "" {
			obs.ActionIDs = append(obs.ActionIDs, o.id)
		}
		switch {
		case o.code == "intent_replay" && o.status == 403:
			replays++
		case o.code == "" && o.id != "":
			if harness.Str(h.WaitSealed(h.Acme, o.id), "execution", "status") == "succeeded" {
				executed++
			}
		}
	}
	obs.Evidence["concurrent"] = map[string]any{"executed": executed, "intent_replay": replays}
	if r := h.Acme.Admin.Do("PATCH", "/v1/agents/"+a.ID, map[string]string{"intent_mode": "off"}); r.Status != 200 {
		h.Problem("intent_mode back to off: HTTP %d", r.Status)
	}
	if seqOK && executed == 1 && replays == 15 && r0.Status == 403 {
		obs.Outcome, obs.Detail = "REJECTED", "REJECTED (intent_replay); the first use executes exactly once"
		obs.Line = "intent_replay · 1 of 16 executed"
	} else {
		obs.Outcome, obs.Detail = "REJECTED?", fmt.Sprintf("sequential ok %t, concurrent executed %d of 16, replays %d, unsigned %d %s",
			seqOK, executed, replays, r0.Status, r0.ErrorCode())
	}
	return obs
}

// J04: 200 calls released together against calls_per_minute 60. Exactly 60 run; 140 are denied
// with budget_exceeded, each recorded and sealed.
func j04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"orders", "other", "secret-echo", "mcp"}}
	a := h.Acme.Agents["burst-bot"]
	// Stay clear of a minute boundary, so all 200 land in one budget window.
	if s := time.Now().Second(); s > 45 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
	type out struct {
		status int
		code   string
		id     string
		msg    string
	}
	outs := make([]out, 200)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			r := a.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": echo(fmt.Sprint(i)), "mode": "gateway", "wait": 0})
			m := r.Map()
			id := harness.Str(m, "id")
			if id == "" {
				id = harness.Str(m, "error", "action", "id")
			}
			outs[i] = out{r.Status, r.ErrorCode(), id, harness.Str(m, "error", "message")}
		}(i)
	}
	start := time.Now()
	close(gate)
	wg.Wait()
	wall := time.Since(start)
	accepted, budget, other := 0, 0, 0
	var msg string
	var acceptedIDs []string
	for _, o := range outs {
		if o.id != "" {
			obs.ActionIDs = append(obs.ActionIDs, o.id)
		}
		switch {
		case o.code == "budget_exceeded" && o.status == 429:
			budget++
			msg = o.msg
		case o.code == "":
			accepted++
			acceptedIDs = append(acceptedIDs, o.id)
		default:
			other++
		}
	}
	executed := 0
	for _, id := range acceptedIDs {
		if harness.Str(h.WaitSealed(h.Acme, id), "execution", "status") == "succeeded" {
			executed++
		}
	}
	// Every refusal is a sealed deny with the budget code.
	sealedDenials := 0
	for _, o := range outs {
		if o.code == "budget_exceeded" && o.id != "" {
			act := h.WaitSealed(h.Acme, o.id)
			if harness.Str(act, "decision", "outcome") == "deny" && harness.Str(act, "decision", "code") == "budget_exceeded" && harness.Str(act, "deed") != "" {
				sealedDenials++
			}
		}
	}
	usage := h.Env.CPQuery(`SELECT coalesce(max(value), 0)::text FROM deedglass.usage_counters WHERE org_id = $1 AND scope LIKE '%' || $2 || '%' AND window_kind = 'minute'`,
		h.Acme.ID, a.ID)
	obs.Evidence = map[string]any{"accepted": accepted, "executed": executed, "budget_exceeded": budget, "other": other,
		"sealed_denials": sealedDenials, "usage_counter": usage, "wall_ms": wall.Milliseconds(), "message": msg}
	if executed == 60 && budget == 140 && sealedDenials == 140 && other == 0 && len(usage) == 1 && usage[0][0] == "60" {
		obs.Outcome, obs.Detail = "DENIED", "DENIED (budget_exceeded) after exactly 60 executed"
		obs.Line = fmt.Sprintf("budget_exceeded · %d executed · %d denied · %d ms", executed, budget, wall.Milliseconds())
	} else {
		obs.Outcome = "DENIED?"
		obs.Detail = fmt.Sprintf("executed %d, budget_exceeded %d (sealed %d), other %d, usage %v", executed, budget, sealedDenials, other, usage)
	}
	return obs
}
